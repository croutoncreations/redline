import AppKit

enum ProviderArtwork {
    static func image(for provider: String, template: Bool, size: CGFloat) -> NSImage {
        let resource = provider.lowercased() == "claude" ? "claude" : "codex"
        let developmentURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .appending(path: "Resources/\(resource).svg")
        let url = Bundle.main.url(forResource: resource, withExtension: "svg") ?? developmentURL
        guard let source = NSImage(contentsOf: url),
              let image = source.copy() as? NSImage else {
            return NSImage(systemSymbolName: "questionmark.circle", accessibilityDescription: provider) ?? NSImage()
        }
        image.size = NSSize(width: size, height: size)
        image.isTemplate = template
        image.accessibilityDescription = provider
        return image
    }
}
