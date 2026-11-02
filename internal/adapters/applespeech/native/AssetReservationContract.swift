import Foundation
import Darwin

@main
struct AssetReservationContract {
    static func main() async {
        let locale = Locale(identifier: "zh-HK")
        let existing = Locale(identifier: "en-US")
        let preexistingVariant = Locale(identifier: "zh-Hant-HK")
        let underscoreVariant = Locale(identifier: "zh_HK")
        let acquiredVariant = Locale(identifier: "zh_Hant_HK")

        expect(
            findNewlyReservedLocale(requested: locale, before: [locale], after: [underscoreVariant]) == nil,
            "preexisting locale is not owned"
        )

        expect(
            findNewlyReservedLocale(requested: locale, before: [locale], after: [preexistingVariant]) == nil,
            "equivalent script variant is not newly owned"
        )

        let acquired = findNewlyReservedLocale(
            requested: locale,
            before: [existing],
            after: [existing, acquiredVariant]
        )
        expect(acquired?.identifier == acquiredVariant.identifier, "only newly added same-locale reservation is owned")

        expect(
            findNewlyReservedLocale(requested: locale, before: [existing], after: [existing, Locale(identifier: "fr-FR")]) == nil,
            "unrelated locale is not owned"
        )

        var released: [String] = []
        let didRelease = await releaseUncommittedReservation(acquired, preparationReachedReady: false) { reservation in
            released.append(reservation.identifier)
            return true
        }
        expect(didRelease && released == [acquiredVariant.identifier], "not-ready failure releases exactly the new locale")

        let didReleasePreexisting = await releaseUncommittedReservation(nil, preparationReachedReady: false) { reservation in
            released.append(reservation.identifier)
            return true
        }
        expect(!didReleasePreexisting && released.count == 1, "preexisting reservation cleanup is a no-op")

        let didReleaseReady = await releaseUncommittedReservation(acquired, preparationReachedReady: true) { reservation in
            released.append(reservation.identifier)
            return true
        }
        expect(!didReleaseReady && released.count == 1, "successful preparation retains its reservation")
    }

    private static func expect(_ condition: Bool, _ message: String) {
        guard condition else {
            fputs("FAIL \(message)\n", stderr)
            exit(EXIT_FAILURE)
        }
        print("PASS \(message)")
    }
}
