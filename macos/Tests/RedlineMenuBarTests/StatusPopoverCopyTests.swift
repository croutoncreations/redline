import Testing
@testable import RedlineMenuBar

@Test func dispatchTierLabelsUseSurplusFirstLanguage() {
    #expect(dispatchTierLabel("behind") == "standard surplus")
    #expect(dispatchTierLabel("well_behind") == "high surplus")
    #expect(dispatchTierLabel("expiring") == "near expiry")
    #expect(dispatchTierLabel("custom_tier") == "custom tier")
}
