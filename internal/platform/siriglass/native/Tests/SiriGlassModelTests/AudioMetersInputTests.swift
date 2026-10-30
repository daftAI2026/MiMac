import XCTest
@testable import SiriGlassModel

final class AudioMetersInputTests: XCTestCase {
    func testAcceptsOriginalMagnitudeAboveOne() {
        let input = AudioMetersInput(
            normalizedPowerAmplitudes: [1.25],
            stopFrequenciesHz: [200]
        )

        XCTAssertNil(input.validationError())
    }

    func testRejectsNegativeAndNonFiniteAmplitudes() {
        for amplitude: Float in [-0.01, .nan, .infinity, -.infinity] {
            let input = AudioMetersInput(
                normalizedPowerAmplitudes: [amplitude],
                stopFrequenciesHz: [200]
            )

            XCTAssertNotNil(input.validationError(), "accepted amplitude \(amplitude)")
        }
    }
}
