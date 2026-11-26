import SwiftUI

/// The hosts and saved Relay Fleet servers, as WindRex's Remote Hosts page
/// lists them: edit to remove or reorder, ⓘ for details, Add Host to pair a
/// single host or Relay Fleet, and Refresh to sync fleet hosts on demand.
struct RemoteHostsView: View {
    @Environment(HostStore.self) private var store
    @Environment(RelayStore.self) private var relays
    @State private var sheet: Sheet?
    @State private var notice: (title: String, message: String)?

    private enum Sheet: Identifiable {
        case connect, manual, scan, details(HostConfig), relayDetails(UUID)
        var id: String {
            switch self {
            case .connect: "connect"
            case .manual: "manual"
            case .scan: "scan"
            case .details(let c): "host-\(c.id.uuidString)"
            case .relayDetails(let id): "relay-\(id.uuidString)"
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
                .onDelete { offsets in
                    for idx in offsets {
                        let config = hosts[idx].config
                        relays.noteHostRemoved(config)
                        store.remove(config.id)
                    }
                }
                .onMove(perform: store.move)
                .listRowBackground(Color.card)
            }
            if !relays.relays.isEmpty {
                Section {
                    ForEach(relays.relays) { relay in
                        RelayRow(
                            relay: relay,
                            hostCount: relays.hostCount(for: relay.id, in: store),
                            isSyncing: relays.syncing.contains(relay.id),
                            error: relays.lastErrors[relay.id],
                            sync: {
                                Task { _ = try? await relays.sync(relayID: relay.id, into: store) }
                            },
                            info: { sheet = .relayDetails(relay.id) }
                        )
                    }
                    .listRowBackground(Color.card)
                } header: {
                    Text("Relays")
                } footer: {
                    Text("Saved relays automatically discover and sync new hosts on pull-to-refresh and app launch.")
                }
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
        .task { await relays.syncAll(into: store, force: false) }
        .alert(
            notice?.title ?? "",
            isPresented: Binding { notice != nil } set: { if !$0 { notice = nil } }
        ) {
        } message: {
            Text(notice?.message ?? "")
        }
        // A sheet of an iOS app on a Mac loses the objects the app
        // injected at the root; both stores go back in for its views.
        .sheet(item: $sheet) { s in
            Group {
                switch s {
                case .connect:
                    ConnectSheet(manual: { sheet = .manual }, scan: { sheet = .scan })
                case .manual:
                    HostForm(config: nil, onAddRelay: { payload in
                        sheet = nil
                        Task { await importRelayFleet(payload) }
                    })
                case .scan:
                    QRScanner(onScan: pair)
                case .details(let c):
                    HostForm(config: c)
                case .relayDetails(let id):
                    RelayDetailSheet(relayID: id)
                }
            }
            .environment(store)
            .environment(relays)
        }
    }

    /// Adds the host of a scanned code, updates it when already configured, or
    /// saves & syncs a Relay Fleet `#r=` QR code.
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
        case .relayFleet(let payload):
            sheet = nil
            Task { await importRelayFleet(payload) }
            return nil
        case .invalid(let why):
            return why
        }
    }

    private func importRelayFleet(_ payload: RelayPairingPayload) async {
        do {
            let summary = try await relays.pairAndSync(payload, into: store)
            notice = (String(localized: "Relay Connected"), summary.userMessage)
        } catch {
            notice = (String(localized: "Couldn't Sync Relay Fleet"), error.localizedDescription)
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

private struct RelayRow: View {
    let relay: RelayConfig
    let hostCount: Int
    let isSyncing: Bool
    let error: String?
    let sync: () -> Void
    let info: () -> Void

    var body: some View {
        HStack(spacing: 14) {
            Image(systemName: "dot.radiowaves.left.and.right")
                .font(.title3)
                .foregroundStyle(.tint)
                .frame(width: 30)
            VStack(alignment: .leading, spacing: 2) {
                Text(relay.label)
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                if isSyncing {
                    Text("Syncing…")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                } else if let error {
                    Text(error)
                        .font(.footnote)
                        .foregroundStyle(.red)
                        .lineLimit(2)
                } else {
                    Text(subtitle)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            .layoutPriority(1)
            .alignmentGuide(.listRowSeparatorLeading) { $0[.leading] }
            Spacer(minLength: 8)
            if isSyncing {
                ProgressView()
                    .controlSize(.small)
            } else {
                Button(action: sync) {
                    Image(systemName: "arrow.clockwise")
                        .font(.body)
                        .foregroundStyle(.tint)
                }
                .buttonStyle(.borderless)
                .accessibilityLabel("Sync Relay")
            }
            Button(action: info) {
                Image(systemName: "info.circle")
                    .font(.title3)
                    .foregroundStyle(.tint)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel("Relay Details")
        }
    }

    private var subtitle: String {
        let countText = String(localized: "\(hostCount) hosts")
        if let lastSync = relay.lastSyncAt {
            let relative = lastSync.formatted(.relative(presentation: .named))
            return String(localized: "\(countText) · Synced \(relative)")
        }
        return String(localized: "\(countText) · Not synced yet")
    }
}

private struct RelayDetailSheet: View {
    @Environment(HostStore.self) private var store
    @Environment(RelayStore.self) private var relays
    @Environment(\.dismiss) private var dismiss
    let relayID: UUID

    @State private var label: String = ""
    @State private var autoSync: Bool = true
    @State private var initialized = false
    @State private var removing = false
    @State private var syncFeedback: String?

    private var current: RelayConfig? { relays.relay(relayID) }

    var body: some View {
        NavigationStack {
            if let relay = current {
                let isSyncing = relays.syncing.contains(relay.id)
                let discovered = relays.lastDiscovered[relay.id] ?? []
                let ignoredSet = Set(relay.ignoredHostIDs)
                let dirty = label.trimmingCharacters(in: .whitespacesAndNewlines) != relay.label
                    || autoSync != relay.autoSync

                Form {
                    Section("Relay Details") {
                        LabeledContent("Label") {
                            TextField(relay.url.host() ?? "Relay", text: $label)
                                .multilineTextAlignment(.trailing)
                                .autocorrectionDisabled()
                        }
                        LabeledContent("Address") {
                            Text(relay.url.absoluteString)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .truncationMode(.middle)
                        }
                        LabeledContent(
                            "TLS Certificate",
                            value: relay.tlsPin != nil
                                ? String(localized: "Pinned")
                                : String(localized: "System CA")
                        )
                        Toggle("Auto-Sync Hosts", isOn: $autoSync)
                    }
                    .listRowBackground(Color.card)

                    Section {
                        LabeledContent(
                            "Synced Hosts",
                            value: "\(relays.hostCount(for: relay.id, in: store))"
                        )
                        if let lastSync = relay.lastSyncAt {
                            LabeledContent("Last Sync") {
                                Text(lastSync, format: .dateTime.month().day().hour().minute())
                                    .foregroundStyle(.secondary)
                            }
                        } else {
                            LabeledContent("Last Sync", value: String(localized: "Never"))
                        }
                        if let err = relays.lastErrors[relay.id] {
                            Text(err)
                                .font(.footnote)
                                .foregroundStyle(.red)
                        }
                        if let syncFeedback {
                            Text(syncFeedback)
                                .font(.footnote)
                                .foregroundStyle(.secondary)
                        }
                        Button {
                            syncFeedback = nil
                            Task {
                                do {
                                    let summary = try await relays.sync(relayID: relay.id, into: store)
                                    syncFeedback = summary.userMessage
                                } catch {
                                    syncFeedback = nil
                                }
                            }
                        } label: {
                            HStack {
                                Label("Sync Now", systemImage: "arrow.clockwise")
                                Spacer()
                                if isSyncing { ProgressView().controlSize(.small) }
                            }
                        }
                        .disabled(isSyncing)
                    } header: {
                        Text("Sync Status")
                    }
                    .listRowBackground(Color.card)

                    if !discovered.isEmpty {
                        Section("Online Hosts") {
                            ForEach(discovered) { host in
                                HStack(spacing: 12) {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(host.displayName)
                                            .foregroundStyle(.primary)
                                            .lineLimit(1)
                                        if host.displayName != host.id {
                                            Text(host.id)
                                                .font(.caption.monospaced())
                                                .foregroundStyle(.secondary)
                                                .lineLimit(1)
                                        }
                                    }
                                    Spacer(minLength: 8)
                                    if !host.fleetAuth {
                                        Label("Individual QR", systemImage: "lock.fill")
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                    } else if ignoredSet.contains(host.id) {
                                        Text("Ignored")
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                    } else {
                                        Label("Auto-Synced", systemImage: "checkmark.circle.fill")
                                            .font(.caption)
                                            .foregroundStyle(.green)
                                    }
                                }
                            }
                        }
                        .listRowBackground(Color.card)
                    }

                    if !relay.ignoredHostIDs.isEmpty {
                        Section {
                            Button("Restore \(relay.ignoredHostIDs.count) Ignored Hosts") {
                                relays.resetIgnoredHosts(for: relay.id)
                                Task {
                                    if let summary = try? await relays.sync(relayID: relay.id, into: store) {
                                        syncFeedback = summary.userMessage
                                    }
                                }
                            }
                        } footer: {
                            Text("Deleted relay hosts are ignored on future syncs until you restore them here.")
                        }
                        .listRowBackground(Color.card)
                    }

                    Section {
                        Button("Remove Relay", role: .destructive) { removing = true }
                            .frame(maxWidth: .infinity)
                            .confirmationDialog(
                                "Remove \(relay.label)?",
                                isPresented: $removing,
                                titleVisibility: .visible
                            ) {
                                Button("Remove Relay", role: .destructive) {
                                    relays.remove(relay.id)
                                    dismiss()
                                }
                            } message: {
                                Text("Auto-sync will stop and the saved relay secret will be deleted. Imported hosts stay in your list.")
                            }
                    }
                    .listRowBackground(Color.card)
                }
                .scrollContentBackground(.hidden)
                .background(Color.page)
                .navigationTitle(relay.label)
                .navigationBarTitleDisplayMode(.inline)
                .onAppear {
                    guard !initialized else { return }
                    initialized = true
                    label = relay.label
                    autoSync = relay.autoSync
                }
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Close", systemImage: "xmark", role: .close) { dismiss() }
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Save", systemImage: "checkmark", role: .confirm) {
                            var updated = relay
                            let trimmed = label.trimmingCharacters(in: .whitespacesAndNewlines)
                            if !trimmed.isEmpty { updated.label = trimmed }
                            updated.autoSync = autoSync
                            relays.update(updated)
                            dismiss()
                        }
                        .disabled(!dirty || label.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                }
            }
        }
        .presentationDetents([.large])
    }
}

private struct ConnectSheet: View {
    let manual: () -> Void
    let scan: () -> Void

    var body: some View {
        VStack(spacing: 12) {
            Text("Connect to Remote Host").font(.title2.bold())
            Text("Scan a host or Relay Fleet QR code from Settings › Server on your Mac, or paste a pairing link manually.")
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

/// A host's address, icon, name and startup command, as WindRex's Edit Host
/// sheet has them: a new host's (or pasted Relay Fleet `#r=` link), or an
/// existing host's to change or delete.
private struct HostForm: View {
    @Environment(HostStore.self) private var store
    @Environment(RelayStore.self) private var relays
    @Environment(\.dismiss) private var dismiss
    let config: HostConfig?
    var onAddRelay: ((RelayPairingPayload) -> Void)? = nil
    @State private var address: String
    @State private var symbol: String
    @State private var name: String
    @State private var command: String
    @State private var deleting = false

    init(config: HostConfig?, onAddRelay: ((RelayPairingPayload) -> Void)? = nil) {
        self.config = config
        self.onAddRelay = onAddRelay
        _address = State(initialValue: config?.url.absoluteString ?? "")
        _symbol = State(initialValue: config?.symbol ?? "laptopcomputer")
        _name = State(initialValue: config?.name ?? "")
        _command = State(initialValue: config?.command ?? "")
    }

    /// Detects a pasted `#r=` Relay Fleet URL when adding a new entry.
    private var enteredRelay: RelayPairingPayload? {
        guard config == nil else { return nil }
        let s = address.trimmingCharacters(in: .whitespacesAndNewlines)
        guard var payload = RelayPairingPayload(pairing: s) else { return nil }
        let n = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if !n.isEmpty {
            payload.label = n
            payload.hasExplicitLabel = true
        }
        return payload
    }

    /// The host as entered, if its address reads as a pairing code does;
    /// a bare host gets http://.
    private var entered: HostConfig? {
        guard enteredRelay == nil else { return nil }
        let s = address.trimmingCharacters(in: .whitespaces)
        guard var c = HostConfig(pairing: s.contains("://") ? s : "http://" + s) else { return nil }
        if let config {
            c.id = config.id
            // The address shown has no token or pin: keep the host's saved
            // credentials unless a new pairing URL with them is pasted.
            if c.lanToken == nil, c.url == config.url { c.lanToken = config.lanToken }
            if c.tlsPin == nil, c.url == config.url { c.tlsPin = config.tlsPin }
            if c.url == config.url {
                c.relayID = config.relayID
                c.relayHostID = config.relayHostID
            }
            c.customName = config.customName
        }
        let n = name.trimmingCharacters(in: .whitespaces)
        if !n.isEmpty {
            if let config, n != config.name {
                c.customName = true
            }
            c.name = n
        }
        c.icon = symbol
        c.desktop = symbol == "desktopcomputer"
        let cmd = command.trimmingCharacters(in: .whitespaces)
        c.command = cmd.isEmpty ? nil : cmd
        return c
    }

    var body: some View {
        let relayPayload = enteredRelay
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
                        Text(config == nil ? "Enter the address of a WindRex server or paste a Relay Fleet link." : "Update the details for this remote host.")
                            .font(.body).textCase(nil)
                            .frame(maxWidth: .infinity).multilineTextAlignment(.center)
                        Text("Server address").frame(maxWidth: .infinity, alignment: .leading)
                    }
                } footer: {
                    if let why = HostConfig.refusal(of: address) {
                        Text(why)
                    } else if relayPayload != nil {
                        Text("Relay Fleet link detected. Adding it saves the relay and automatically imports online hosts.")
                    }
                }
                .listRowBackground(Color.card)
                if let relayPayload {
                    Section("Relay Fleet") {
                        TextField(relayPayload.label, text: $name)
                            .autocorrectionDisabled()
                        LabeledContent("Address") {
                            Text(relayPayload.url.absoluteString)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .truncationMode(.middle)
                        }
                        LabeledContent(
                            "TLS Certificate",
                            value: relayPayload.tlsPin != nil
                                ? String(localized: "Pinned")
                                : String(localized: "System CA")
                        )
                    }
                    .listRowBackground(Color.card)
                } else {
                    Section {
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
                    } header: {
                        Text("Host details")
                    } footer: {
                        if let relayID = config?.relayID {
                            let label = relays.relay(relayID)?.label ?? String(localized: "Relay")
                            Text("Synced from \(label).")
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
                }
                if let config {
                    Section {
                        Button("Delete Host", role: .destructive) { deleting = true }
                            .frame(maxWidth: .infinity)
                            .confirmationDialog("Delete \(config.name)?", isPresented: $deleting, titleVisibility: .visible) {
                                Button("Delete Host", role: .destructive) {
                                    relays.noteHostRemoved(config)
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
                        if let relayPayload {
                            if let onAddRelay {
                                onAddRelay(relayPayload)
                            } else {
                                Task { _ = try? await relays.pairAndSync(relayPayload, into: store) }
                                dismiss()
                            }
                            return
                        }
                        guard let c else { return }
                        if config == nil { store.add(c) } else { store.update(c) }
                        dismiss()
                    }
                    .disabled((relayPayload == nil && c == nil) || (config != nil && c == config))
                }
            }
        }
        .presentationDetents([.large])
    }
}
