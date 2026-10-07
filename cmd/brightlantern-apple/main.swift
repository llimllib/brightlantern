// brightlantern-apple answers one prompt with Apple's on-device model.
//
// A separate process rather than code linked into brightlantern. A crash inside
// cgo cannot be recovered, and AGENTS.md's sqlite-lembed section is what that
// costs; a crashed helper costs one title. It also keeps the Go side buildable
// and testable on Linux, where this does not exist.
//
// Deliberately generic: it knows nothing about titles. The instructions come
// from Go, so the prompt has one definition shared by every backend.
//
// Protocol:
//
//   brightlantern-apple --check
//       exit 0 if the model is available, otherwise exit 3 with the reason
//       on stderr.
//
//   brightlantern-apple < {"instructions": "...", "prompt": "...", "max_tokens": 40}
//       the response on stdout, exit 0.
//       exit 3: the model is unavailable on this machine.
//       exit 4: the model declined this input -- a guardrail, a refusal, too
//               long, an unsupported language. Asking again gets the same
//               answer, so the caller should not.
//       exit 1: anything else, including rate limiting. Worth retrying later.
//
// Built with plain swiftc from Command Line Tools: `mise run apple`.

import Foundation
import FoundationModels

struct Request: Decodable {
    let instructions: String
    let prompt: String
    let max_tokens: Int?
}

func fail(_ code: Int32, _ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(code)
}

func checkAvailable() {
    switch SystemLanguageModel.default.availability {
    case .available:
        return
    case .unavailable(.deviceNotEligible):
        fail(3, "this Mac cannot run Apple Intelligence")
    case .unavailable(.appleIntelligenceNotEnabled):
        fail(3, "Apple Intelligence is turned off; enable it in System Settings > Apple Intelligence & Siri")
    case .unavailable(.modelNotReady):
        fail(3, "Apple Intelligence is still downloading its model; try again later")
    case .unavailable(let other):
        fail(3, "Apple Intelligence is unavailable: \(other)")
    }
}

checkAvailable()
if CommandLine.arguments.dropFirst().contains("--check") {
    exit(0)
}

let input = FileHandle.standardInput.readDataToEndOfFile()
let req: Request
do {
    req = try JSONDecoder().decode(Request.self, from: input)
} catch {
    fail(1, "bad request on stdin: \(error)")
}

// A session per process, and so per prompt: nothing from one transcript can
// leak into the next one's answer.
let session = LanguageModelSession(instructions: req.instructions)
// Greedy, because the same transcript should get the same title every time it
// is asked; nothing here benefits from variety.
//
// sampling:, not samplingMode:. The SDK 27 rename does not exist in SDK 26,
// which is what the macos-26 runners build with, and using it broke the
// v0.0.3 release; SDK 27 accepts the old label with a deprecation warning.
let options = GenerationOptions(sampling: .greedy, maximumResponseTokens: req.max_tokens ?? 64)

do {
    let response = try await session.respond(to: req.prompt, options: options)
    print(response.content)
} catch let err as LanguageModelSession.GenerationError {
    switch err {
    case .guardrailViolation, .refusal, .exceededContextWindowSize, .unsupportedLanguageOrLocale:
        fail(4, "declined: \(err)")
    default:
        fail(1, "\(err)")
    }
} catch {
    fail(1, "\(error)")
}
