import SwiftUI

/// The hosts, as Rex's Remote Hosts page lists them: edit to remove or
/// reorder, ⓘ for one's details, Add Host to pair or set one up.
struct RemoteHostsView: View {
    @Environment(HostStore.self) private var store
    @State private var sheet: Sheet?

    private enum Sheet: Identifiable {
        case connect, manual, scan, details(HostConfig)
        var id: String {
            switch self {
            case .connect: "connect"
            case .manual: "manual"
            case .scan: "scan"
            case .details(let c): c.id.uuidString
            }
        }
    }

    var body: some View {
        // Demo Mode's host runs on the phone, so it is no host to edit here.
        let hosts = store.hosts.filter { !$0.isDemo }
        List {
            if store.demo != nil {
                Section {
                    Button("Leave Demo Mode", systemImage: "sparkles", role: .destructive) {
                        store.exitDemo()
                    }
                    .listRowBackground(Color.card)
                } footer: {
                    Text("You're in demo mode. Add a host to run your own tabs.")
                }
            }
            Section("Hosts") {
                if hosts.isEmpty {
                    EmptyState("No hosts yet", symbol: "desktopcomputer")
                        .listRowBackground(Color.card)
                }
                ForEach(hosts) { h in
                    HostRow(host: h) { sheet = .details(h.config) }
                }
                .onDelete { $0.map { hosts[$0].id }.forEach(store.remove) }
                .onMove(perform: store.move)
                .listRowBackground(Color.card)
            }
            Section {
                Button { sheet = .connect } label: {
                    Label {
                        Text("Add Host")
                    } icon: {
                        Image(systemName: "globe").overlay(alignment: .bottomTrailing) {
                            Image(systemName: "plus.circle.fill").font(.system(size: 9, weight: .bold))
                                .background(Color.card, in: Circle()).offset(x: 3, y: 3)
                        }
                    }
                }
                .foregroundStyle(.primary)
                .listRowBackground(Color.card)
            }
        }
        .scrollContentBackground(.hidden)
        .background(Color.page)
        .navigationTitle("Remote Hosts")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { EditButton() }
        // A sheet of an iOS app on a Mac loses the objects the app
        // injected at the root; the store goes back in for its views.
        .sheet(item: $sheet) { s in
            Group {
                switch s {
                case .connect:
                    ConnectSheet(manual: { sheet = .manual }, scan: { sheet = .scan })
                case .manual:
                    HostForm(config: nil)
                case .scan:
                    QRScanner(onScan: pair)
                case .details(let c):
                    HostForm(config: c)
                }
            }
            .environment(store)
        }
    }

    /// Adds the host of a scanned code, or updates and shows it when it is
    /// already configured.
    private func pair(_ code: String) -> String? {
        switch HostPairing.resolve(code, existing: store.hosts.map(\.config)) {
        case .existing(let updated):
            if let cur = store.host(updated.id), cur.config != updated {
                store.update(updated)
            }
            sheet = .details(updated)
            return nil
        case .added(let config):
            store.add(config)
            sheet = nil
            return nil
        case .invalid(let why):
            return why
        }
    }
}

private struct HostRow: View {
    let host: Host
    let info: () -> Void

    var body: some View {
        HStack(spacing: 14) {
            Image(systemName: host.config.symbol)
                .font(.title3).frame(width: 30)
            VStack(alignment: .leading, spacing: 2) {
                Text(host.config.name).foregroundStyle(.primary).lineLimit(1)
                Text(host.config.url.absoluteString)
                    .font(.footnote).foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            .layoutPriority(1)
            .alignmentGuide(.listRowSeparatorLeading) { $0[.leading] }
            Spacer(minLength: 8)
            HStack(spacing: 6) {
                HostStatusDot(status: host.status)
                    .accessibilityHidden(true)
                Text(status)
                    .font(.subheadline)
                    .foregroundStyle(host.status == .online ? .primary : .secondary)
                    .lineLimit(1)
                    .fixedSize()
            }
            .accessibilityElement(children: .combine)
            Button(action: info) {
                Image(systemName: "info.circle")
                    .font(.title3)
                    .foregroundStyle(.tint)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel("Edit Host")
        }
    }

    private var status: LocalizedStringKey {
        switch host.status {
        case .online: "Connected"
        case .connecting: "Connecting…"
        case .offline: "Offline"
        }
    }
}

private struct ConnectSheet: View {
    let manual: () -> Void
    let scan: () -> Void

    var body: some View {
        VStack(spacing: 12) {
            Text("Connect to Remote Host").font(.title2.bold())
            Text("Scan the WindRex QR code from Settings › Server on your Mac, or enter your server details manually.")
                .font(.body).foregroundStyle(.secondary).multilineTextAlignment(.center)
            Spacer(minLength: 12)
            Button(action: manual) { Text("Set Up Manually").frame(maxWidth: .infinity) }
                .buttonStyle(.glass)
            Button(action: scan) { Label("Pair via QR Code", systemImage: "qrcode").frame(maxWidth: .infinity) }
                .buttonStyle(.glassProminent)
                .foregroundStyle(Color.page)
        }
        .font(.headline)
        .controlSize(.extraLarge)
        .padding(24)
        .presentationDetents([.height(320)])
    }
}

/// A host's address, icon, name and startup command, as Rex's Edit Host
/// sheet has them: a new host's, or one's to change or delete.
private struct HostForm: View {
    @Environment(HostStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    let config: HostConfig?
    @State private var address: String
    @State private var symbol: String
    @State private var name: String
    @State private var command: String
    @State private var deleting = false

    init(config: HostConfig?) {
        self.config = config
        _address = State(initialValue: config?.url.absoluteString ?? "")
        _symbol = State(initialValue: config?.symbol ?? "laptopcomputer")
        _name = State(initialValue: config?.name ?? "")
        _command = State(initialValue: config?.command ?? "")
    }

    /// The host as entered, if its address reads as a pairing code does;
    /// a bare host gets http://.
    private var entered: HostConfig? {
        let s = address.trimmingCharacters(in: .whitespaces)
        guard var c = HostConfig(pairing: s.contains("://") ? s : "http://" + s) else { return nil }
        if let config {
            c.id = config.id
            // The address shown has no token or pin: keep the host's saved
            // credentials unless a new pairing URL with them is pasted.
            if c.lanToken == nil, c.url == config.url { c.lanToken = config.lanToken }
            if c.tlsPin == nil, c.url == config.url { c.tlsPin = config.tlsPin }
        }
        let n = name.trimmingCharacters(in: .whitespaces)
        if !n.isEmpty { c.name = n }
        c.icon = symbol
        c.desktop = symbol == "desktopcomputer"
        let cmd = command.trimmingCharacters(in: .whitespaces)
        c.command = cmd.isEmpty ? nil : cmd
        return c
    }

    var body: some View {
        let c = entered
        NavigationStack {
            Form {
                Section {
                    TextField("http://192.168.1.2:7297", text: $address)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .accessibilityIdentifier("address")
                } header: {
                    VStack(spacing: 20) {
                        Text(config == nil ? "Enter the details of your Rex server." : "Update the details for this remote host.")
                            .font(.body).textCase(nil)
                            .frame(maxWidth: .infinity).multilineTextAlignment(.center)
                        Text("Server address").frame(maxWidth: .infinity, alignment: .leading)
                    }
                } footer: {
                    if let why = HostConfig.refusal(of: address) { Text(why) }
                }
                .listRowBackground(Color.card)
                Section("Host details") {
                    HStack(spacing: 12) {
                        Menu {
                            Picker("Device", selection: $symbol) {
                                ForEach(HostConfig.icons, id: \.symbol) { Label($0.name, systemImage: $0.symbol).tag($0.symbol) }
                            }
                        } label: {
                            HStack(spacing: 4) {
                                Image(systemName: symbol)
                                Image(systemName: "chevron.up.chevron.down").font(.caption2.weight(.semibold))
                            }
                            .foregroundStyle(.primary)
                        }
                        .accessibilityLabel("Device")
                        TextField(c.map { HostConfig.name($0.url.host() ?? "") } ?? String(localized: "Name"), text: $name)
                            .autocorrectionDisabled()
                    }
                }
                .listRowBackground(Color.card)
                Section {
                    TextField("Default login shell", text: $command)
                        .font(.body.monospaced())
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                } header: {
                    Text("Startup command")
                } footer: {
                    Text("Executable and arguments for new terminals on this host. Use quotes to group arguments. Leave empty to use the host's default login shell.")
                }
                .listRowBackground(Color.card)
                if let config {
                    Section {
                        Button("Delete Host", role: .destructive) { deleting = true }
                            .frame(maxWidth: .infinity)
                            .confirmationDialog("Delete \(config.name)?", isPresented: $deleting, titleVisibility: .visible) {
                                Button("Delete Host", role: .destructive) {
                                    store.remove(config.id)
                                    dismiss()
                                }
                            }
                    }
                    .listRowBackground(Color.card)
                }
            }
            .scrollContentBackground(.hidden)
            .background(Color.page)
            .navigationTitle(config == nil ? "Add Host" : "Edit Host")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", systemImage: "xmark", role: .cancel) { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(config == nil ? "Add" : "Save", systemImage: "checkmark", role: .confirm) {
                        guard let c else { return }
                        if config == nil { store.add(c) } else { store.update(c) }
                        dismiss()
                    }
                    .disabled(c == nil || c == config)
                }
            }
        }
        .presentationDetents([.large])
    }
}
