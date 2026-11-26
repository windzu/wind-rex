import SwiftUI

/// Diagnostics for a host connection, for a problem report: why a terminal
/// failed, the host's address and URL, and the recent connection log. Copy
/// takes it all as text.
struct DiagnosticsView: View {
    /// The host's address.
    let address: String
    /// The host of a terminal.
    var host: HostConfig?
    /// Why the terminal failed (TerminalSession.diagnosis).
    var failure: String?

    @State private var copied = false

    // What the sheet shows is for whoever reads the report: not localized,
    // but for its headings.
    private var sections: [(title: LocalizedStringResource, text: String)] {
        let build = Bundle.main.infoDictionary?["CFBundleVersion"] as? String ?? "0"
        var app = ["WindRex \(Bundle.main.version) (\(build)), iOS \(UIDevice.current.systemVersion)", Date.now.ISO8601Format()]
        app.append("Address \(address)")
        if let host {
            app.append("URL \(host.url.absoluteString)")
        }
        let all: [(title: LocalizedStringResource, text: String)?] = [
            failure.map { ("Failure", $0) },
            ("Host", app.joined(separator: "\n")),
            ("Log", ConnectionLog.lines.joined(separator: "\n")),
        ]
        return all.compactMap { $0 }
    }

    var body: some View {
        List {
            ForEach(sections, id: \.title.key) { s in
                Section {
                    Text(s.text).font(.caption.monospaced()).textSelection(.enabled)
                } header: {
                    Text(s.title)
                }
                .listRowBackground(Color.card)
            }
        }
        .scrollContentBackground(.hidden)
        .background(Color.page)
        .navigationTitle("Diagnostics")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button(copied ? "Copied" : "Copy", systemImage: copied ? "checkmark" : "doc.on.doc") {
                    UIPasteboard.general.string = sections.map { "\($0.title.key)\n\($0.text)" }.joined(separator: "\n\n")
                    copied = true
                }
            }
        }
        .task(id: copied) {
            try? await Task.sleep(for: .seconds(1.5))
            copied = false
        }
        .sensoryFeedback(.success, trigger: copied) { _, new in new }
    }
}
