import Foundation
import Speech
import Darwin

@main
struct AssetErrorClassificationContract {
    static func fail(_ message: String) -> Never {
        FileHandle.standardError.write(Data(("FAIL " + message + "\n").utf8))
        exit(1)
    }

    static func main() {
        guard #available(macOS 26.0, *) else { fail("requires macOS 26 SDK") }
        let cases: [(String, Error, AssetInventory.Status, String, String)] = [
            (
                "no model is unsupported",
                NSError(domain: SFSpeechError.errorDomain, code: SFSpeechError.Code.noModel.rawValue,
                        userInfo: [NSLocalizedDescriptionKey: "No model is available or downloadable"]),
                .supported,
                "assets unsupported:",
                "assets unavailable:"
            ),
            (
                "locale reservation is actionable missing setup",
                NSError(domain: SFSpeechError.errorDomain, code: SFSpeechError.Code.assetLocaleNotAllocated.rawValue,
                        userInfo: [NSLocalizedDescriptionKey: "Locale not allocated"]),
                .supported,
                "assets unavailable:",
                "assets unsupported:"
            ),
            (
                "ordinary format error stays ordinary",
                NSError(domain: SFSpeechError.errorDomain, code: SFSpeechError.Code.incompatibleAudioFormats.rawValue,
                        userInfo: [NSLocalizedDescriptionKey: "Incompatible audio formats"]),
                .supported,
                "Error Domain=",
                "assets unavailable:"
            ),
            (
                "explicit unsupported status remains unsupported",
                NSError(domain: "ordinary", code: 73, userInfo: [NSLocalizedDescriptionKey: "prepare failure"]),
                .unsupported,
                "assets unsupported:",
                "assets unavailable:"
            ),
        ]
        for (name, error, status, wantPrefix, rejectPrefix) in cases {
            let classified = String(describing: classifyPreparationFailure(error, status: status, locale: "zh-HK"))
            guard classified.hasPrefix(wantPrefix), !classified.hasPrefix(rejectPrefix) else {
                fail("\(name): got \(classified)")
            }
            print("PASS \(name): \(classified)")
        }

        let quota = NSError(domain: SFSpeechError.errorDomain, code: SFSpeechError.Code.tooManyAssetLocalesAllocated.rawValue,
                            userInfo: [NSLocalizedDescriptionKey: "No locale reservations available"])
        let requestFailure = String(describing: classifyInstallationRequestFailure(quota, status: .supported, locale: "zh-HK"))
        guard requestFailure.hasPrefix("asset installation request failed:"), !requestFailure.hasPrefix("assets unavailable:") else {
            fail("reservation quota error misclassified: \(requestFailure)")
        }
        print("PASS reservation failure stays distinct: \(requestFailure)")
    }
}
