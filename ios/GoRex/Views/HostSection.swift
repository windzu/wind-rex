import SwiftUI

/// A host in the list: its name and state, then, for a host that keeps
/// projects (docs/MOD-002-projects.md, WindRex), its projects of the space shown in
/// order of last use, those not used for 30 days under Older, and its tabs
/// with no project, which every space shows, tiled after them. A project
/// opens to its tabs and a New Tab row. A host away shows its projects as
/// last seen, greyed, only to read. A host of before projects lists its
/// sessions and their tabs, as before.
struct HostSection: View {
    let host: Host
    /// The space shown; "" is Default.
    let space: String
    /// The spaces the picker lists, for Move to Space.
    let spaces: [String]
    @Binding var collapsed: Set<String>
    /// The projects and Older open, by key.
    @Binding var expanded: Set<String>
    let query: String
    /// What New Project's sheet zooms out of, and the rows move between
    /// the lists in.
    let zoom: Namespace.ID
    let places: Namespace.ID
    let open: (TerminalTarget) -> Void
    /// Opens a row's tab, from the row.
    let openRow: (TerminalTarget) -> Void
    /// Opens New Project on the host.
    let newProject: () -> Void
    /// Whether this list belongs to a split window with a terminal beside it.
    let showsTerminalAside: Bool
    /// The terminal the window shows beside the list; nil on a narrow
    /// window, where the list is the whole page and no project is current.
    var current: TerminalTarget?
    /// Lifts the section into a drag that reorders the hosts
    /// (docs/IOS-022-host-drag.md); nil where the host cannot move, so
    /// its header has no drag source.
    var liftHost: (() -> NSItemProvider)?
    /// The list's part of a host's drag: while a host is dragged, the
    /// section's text targets pass it over and take it, where a target
    /// wrapping the section would never see it — the deepest target at
    /// a point is the one the drop goes to.
    var hostDrop: HostDrop?
    @Environment(HostStore.self) private var store
    @Environment(RelayStore.self) private var relays
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(SpaceBook.self) private var book
    /// The sessions whose new tab is on its way.
    @State private var opening: Set<String> = []
    /// The row being dragged, and its session's rows in the order it moves
    /// them to: they make way as it passes over them.
    @State private var drag: Drag?
    @State private var order: [String] = []
    /// Each lift, for its feel.
    @State private var lifts = 0
    /// Whether a new session or tab with no project is on its way.
    @State private var creating = false
    /// What failed last, and why, as the server says it.
    @State private var failure: (title: LocalizedStringKey, why: String)?
    /// The tab being renamed, and the name being typed.
    @State private var renaming: (session: String, window: String, title: String)?
    @State private var name = ""
    /// The project being renamed, and the one Close Project asks about.
    @State private var renamingProject: Host.Session?
    @State private var closingProject: Host.Session?
    @State private var renamingHost = false
    /// The project whose folder is gone, which can only be closed.
    @State private var missing: Host.Session?
    /// The project whose pinned quick launchers are being configured.
    @State private var configuringLaunchers: Host.Session?

    /// The key of a project or Older of a host, as expanded keeps it.
    static func key(_ host: Host, _ id: String) -> String { host.id.uuidString + "|" + id }

    private var hostKey: String { host.id.uuidString }

    var body: some View {
        let isCollapsed = collapsed.contains(hostKey)
        VStack(alignment: .leading, spacing: 4) {
            header(isCollapsed)
            if !isCollapsed { content }
        }
        // A row dropped in the section, off the rows, stays where it is;
        // while a host is dragged, the host lands on the section instead.
        .onDrop(of: [.text], delegate: RowDrop(
            pass: { if hostDrop?.active() == true { hostDrop?.pass() } },
            drop: { hostDrop?.active() == true ? hostDrop?.drop($0) ?? false : drop($0) }))
        // The dragged session's server order: a dropped row waits for it to
        // change; a drag ends when its tab goes.
        .onChange(of: drag.map { d in host.sessions.first { $0.id == d.session.id }?.view?.windows?.map(\.window_id) ?? [] }) { _, ids in
            guard let d = drag, let ids else { return }
            if d.dropped ? ids != d.from : !ids.contains(d.window) { drag = nil }
        }
        // A drag let go of with no drop, or off its card's rows.
        .onReceive(NotificationCenter.default.publisher(for: Lifted.ended)) { n in
            if let d = drag, !d.dropped, n.object as? UUID == d.token { drag = nil }
        }
        .modifier(DragEnd { if drag?.dropped == false { drag = nil } })
        .sensoryFeedback(.selection, trigger: lifts)
        .alert(failure?.title ?? "", isPresented: Binding { failure != nil } set: { if !$0 { failure = nil } }) {
        } message: {
            Text(failure?.why ?? "")
        }
        .alert("Rename Tab", isPresented: Binding { renaming != nil } set: { if !$0 { renaming = nil } }, presenting: renaming) { r in
            TextField(r.title, text: $name)
            Button("Cancel", role: .cancel) {}
            Button("Save") {
                change("Can't Rename the Tab") { try await host.renameWindow(r.window, in: r.session, to: name) }
            }
        }
        .alert("Rename Project", isPresented: Binding { renamingProject != nil } set: { if !$0 { renamingProject = nil } }, presenting: renamingProject) { s in
            TextField(s.label, text: $name)
            Button("Cancel", role: .cancel) {}
            Button("Save") {
                let n = name.trimmingCharacters(in: .whitespacesAndNewlines)
                guard !n.isEmpty else { return }
                change("Can't Rename the Project") { try await host.updateProject(s.id, name: n) }
            }
        } message: { _ in
            Text("Only the name changes: the folder and the tabs stay.")
        }
        .sheet(item: $configuringLaunchers) { s in
            ProjectLaunchersSheet(host: host, sessionID: s.id, projectName: s.label) { why in
                failure = ("Can't Update Quick Launchers", why)
            }
        }
        .alert("Rename Host", isPresented: $renamingHost) {
            TextField(host.config.name, text: $name)
            Button("Cancel", role: .cancel) {}
            Button("Save") {
                store.rename(host.id, to: name)
            }
        } message: {
            Text("The name shows on this device only.")
        }
        // Closing a project ends the programs of its tabs: it asks first.
        .confirmationDialog("Close Project", isPresented: Binding { closingProject != nil } set: { if !$0 { closingProject = nil } }, presenting: closingProject) { s in
            Button("Close Project", role: .destructive) {
                change("Can't Close the Project") { try await host.closeSession(s.id) }
            }
        } message: { s in
            Text("Close \(s.label) and every tab in it? The programs in its tabs end.")
        }
        .alert("Folder Not Found", isPresented: Binding { missing != nil } set: { if !$0 { missing = nil } }, presenting: missing) { s in
            Button("Close Project…", role: .destructive) { closingProject = s }
            Button("Cancel", role: .cancel) {}
        } message: { s in
            Text("\(s.project?.dir ?? "") is not on \(host.config.name) any more, so no tab of \(s.label) can open in it.")
        }
    }

    // MARK: Header

    /// The host's row: its name and state, its + for a new project, and
    /// its menu. The row folds the host away.
    private func header(_ isCollapsed: Bool) -> some View {
        HStack(spacing: 10) {
            Image(systemName: host.config.symbol)
                .font(.subheadline)
                .foregroundStyle(.secondary)
            Text(host.config.name).font(.body.weight(.semibold)).lineLimit(1)
            HostStatusDot(status: host.status)
            Spacer()
            if host.status == .online, host.hasProjects, host.listsFolders {
                Button { newProject() } label: {
                    Image(systemName: "plus").frame(width: 30, height: 30)
                }
                .font(.body.weight(.semibold))
                .buttonStyle(.glass)
                .buttonBorderShape(.circle)
                .accessibilityLabel("New Project")
                .matchedTransitionSource(id: ZoomSource.newProject(host.id), in: zoom)
            }
            Menu {
                Button("Rename…", systemImage: "pencil") {
                    name = host.config.name
                    renamingHost = true
                }
                if host.hasProjects {
                    Button("New Tab with No Project", systemImage: "plus") { newLooseTab() }
                        .disabled(creating || host.status != .online)
                } else if !hasProjects {
                    Button("New Session", systemImage: "plus.rectangle.on.rectangle") { newSession() }
                        .disabled(creating)
                }
                Button("Reconnect", systemImage: "arrow.clockwise") { host.resume() }
                Button("Remove", systemImage: "trash", role: .destructive) {
                    relays.noteHostRemoved(host.config)
                    store.remove(host.id)
                }
            } label: {
                Group {
                    if creating { ProgressView() } else { Image(systemName: "ellipsis") }
                }
                .frame(width: 30, height: 30)
            }
            .font(.body.weight(.semibold))
            .buttonStyle(.glass)
            .buttonBorderShape(.circle)
            .accessibilityLabel("Host Menu")
        }
        .foregroundStyle(.primary)
        .frame(minHeight: SidebarListMetrics.rowHeight)
        .contentShape(.rect)
        // A long press lifts the host, to move it in the list.
        .modifier(HostLift(lift: liftHost))
        .onTapGesture {
            withAnimation(.snappy) { if isCollapsed { collapsed.remove(hostKey) } else { collapsed.insert(hostKey) } }
        }
        .accessibilityAddTraits(.isButton)
        .accessibilityHint(isCollapsed ? "Expand" : "Collapse")
    }

    /// Whether the host keeps projects, online or as last seen.
    private var hasProjects: Bool {
        host.hasProjects || book.listings[hostKey] != nil
    }

    // MARK: Content

    @ViewBuilder private var content: some View {
        if host.status == .online {
            if host.hasProjects { projects } else { sessions }
        } else if let l = book.listing(for: hostKey) {
            away(l)
        } else {
            switch host.status {
            case .connecting where host.sessions.isEmpty:
                Text("Connecting…").foregroundStyle(.secondary)
            case .offline:
                offline()
            default:
                sessions
            }
        }
    }

    /// A host that cannot connect. The raw error stays where diagnostics
    /// shows it (NetworkDiagnostics, the developer page's debug info).
    private func offline() -> some View {
        Label("Offline", systemImage: "wifi.slash")
            .symbolRenderingMode(.hierarchical)
            .foregroundStyle(.secondary)
    }

    /// The host's projects of the space, Older, and its tabs with no
    /// project.
    @ViewBuilder private var projects: some View {
        let mine = host.sessions.filter { s in
            s.project.map { book.space($0.space, on: hostKey) == space } ?? false
        }.filter(matches)
        let (recent, older) = ProjectOrder.split(mine, used: { $0.project?.used }, name: \.label)
        let loose = host.sessions.filter { $0.project == nil }
        let looseTabs = entries(loose.flatMap { s in (s.view?.windows ?? []).map { (s, $0) } })
        VStack(spacing: 0) {
            ForEach(recent) { project($0) }
            if !older.isEmpty {
                group("older", Text("Older"), count: older.count) {
                    ForEach(older) { project($0) }
                }
            }
            if query.isEmpty || !looseTabs.isEmpty {
                let key = Self.key(host, "loose")
                let hasTabs = !looseTabs.isEmpty
                let isOpen = !hasTabs || !collapsed.contains(key) || !query.isEmpty
                Button {
                    toggleLoose(key)
                } label: {
                    groupRow(Text("No Project"), open: isOpen, collapsible: hasTabs)
                }
                .buttonStyle(.plain)
                .disabled(!hasTabs)
                .hoverEffect(.highlight)
                .contextMenu { Button("New Tab", systemImage: "plus") { newLooseTab() } }
                .accessibilityHint(hasTabs ? isOpen ? "Collapse" : "Expand" : "")
                if isOpen {
                    VStack(spacing: 0) {
                        rows(looseTabs)
                        newTabRow { newLooseTab() }
                    }
                    .transition(rowTransition)
                }
            }
        }
    }

    /// A project's row and, when open, its tabs and New Tab row.
    private func project(_ s: Host.Session) -> some View {
        let key = Self.key(host, s.id)
        let isOpen = expanded.contains(key) || !query.isEmpty
        return VStack(spacing: 0) {
            Button { toggle(key) } label: {
                ProjectRow(name: s.label, count: s.view?.windows?.count ?? 0, open: isOpen,
                           busy: opening.contains(s.id))
            }
            .buttonStyle(.plain)
            .hoverEffect(.highlight)
            .contextMenu { projectMenu(s) }
            .accessibilityHint(isOpen ? "Collapse" : "Expand")
            if isOpen {
                VStack(spacing: 0) {
                    rows(entries((s.view?.windows ?? []).map { (s, $0) }))
                    projectNewTabRow(s)
                }
                .padding(.leading, SidebarListMetrics.indent)
                .transition(rowTransition)
            }
        }
    }

    private var rowTransition: AnyTransition {
        reduceMotion ? .identity : .opacity.combined(with: .scale(scale: 0.96, anchor: .top))
    }

    private func toggleLoose(_ key: String) {
        withAnimation(reduceMotion ? nil : .snappy) {
            if collapsed.contains(key) { collapsed.remove(key) } else { collapsed.insert(key) }
        }
    }

    /// A project's menu (docs/MOD-002-projects.md, Desktop: a project's context
    /// menu): Reveal Folder is the desktop's only.
    @ViewBuilder private func projectMenu(_ s: Host.Session) -> some View {
        Button("New Tab", systemImage: "plus") { newProjectTab(s) }
        Divider()
        Button("Rename…", systemImage: "pencil") {
            name = s.label
            renamingProject = s
        }
        Menu("Color", systemImage: "paintpalette") {
            ForEach(ProjectColor.all, id: \.id) { c in
                Button {
                    change("Can't Change the Color") { try await host.updateProject(s.id, color: c.id) }
                } label: {
                    if s.project?.color == c.id { Label(Self.colorName(c.id), systemImage: "checkmark") } else { Text(Self.colorName(c.id)) }
                }
            }
            Divider()
            Button("No Color") {
                change("Can't Change the Color") { try await host.updateProject(s.id, color: "default") }
            }
            .disabled(s.project?.color == nil)
        }
        Menu("Move to Space", systemImage: "rectangle.stack") {
            ForEach(spaces, id: \.self) { sp in
                Button(SpaceName.shown(sp)) {
                    change("Can't Move the Project") { try await host.updateProject(s.id, space: sp) }
                }
                .disabled(sp == space)
            }
        }
        if !host.agents.isEmpty {
            Button("Quick Launchers…", systemImage: "bolt") { configuringLaunchers = s }
        }
        Divider()
        Button("Close Project…", systemImage: "xmark", role: .destructive) { closingProject = s }
    }

    /// A color's name, as Color offers it.
    static func colorName(_ id: String) -> String {
        switch id {
        case "rose": String(localized: "Rose")
        case "orange": String(localized: "Orange")
        case "amber": String(localized: "Amber")
        case "green": String(localized: "Green")
        case "teal": String(localized: "Teal")
        case "blue": String(localized: "Blue")
        case "indigo": String(localized: "Indigo")
        case "violet": String(localized: "Violet")
        case "pink": String(localized: "Pink")
        default: id
        }
    }

    /// A heading and the projects below it: Older starts closed.
    private func group(_ id: String, _ title: Text, count: Int, @ViewBuilder content: () -> some View) -> some View {
        let key = Self.key(host, id)
        let isOpen = expanded.contains(key) || !query.isEmpty
        return VStack(spacing: 0) {
            Button { toggle(key) } label: {
                groupRow(title, open: isOpen, count: count)
            }
            .buttonStyle(.plain)
            .accessibilityHint(isOpen ? "Collapse" : "Expand")
            if isOpen { content() }
        }
    }

    private func groupRow(_ title: Text, open: Bool, collapsible: Bool = true, count: Int? = nil) -> some View {
        HStack(spacing: SidebarListMetrics.gap) {
            title.font(.footnote.weight(.semibold))
            Spacer(minLength: 8)
            if collapsible { Chevron(open: open) }
            if let count {
                Text(verbatim: "\(count)").font(.footnote.monospacedDigit())
            }
        }
        .foregroundStyle(.secondary)
        .padding(.horizontal, SidebarListMetrics.inset)
        .frame(minHeight: SidebarListMetrics.rowHeight)
        .contentShape(.rect)
    }

    /// A project's bottom row: the New Tab row of No Project, and, when
    /// the host offers agent CLIs, a right-side cluster of the project's
    /// pinned glyphs and a ▾ menu of them all
    /// (docs/MOD-010-agent-launchers.md).
    @ViewBuilder private func projectNewTabRow(_ s: Host.Session) -> some View {
        if host.agents.isEmpty {
            newTabRow { newProjectTab(s) }
        } else {
            let glyphs = Host.launcherGlyphs(s.project?.launchers, of: host.agents)
            HStack(spacing: 0) {
                Button { newProjectTab(s) } label: {
                    Label("New Tab", systemImage: "plus")
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.leading, SidebarListMetrics.inset)
                        .frame(minHeight: SidebarListMetrics.rowHeight)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .hoverEffect(.highlight)
                ForEach(glyphs) { agent in
                    Button { newProjectTab(s, command: agent.command) } label: {
                        ProgramGlyph(program: .of(agent.id))
                            .foregroundStyle(.secondary)
                            .frame(width: 14, height: 14)
                            .frame(width: 28, height: 44)
                            .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .hoverEffect(.highlight)
                    .accessibilityLabel("New \(agent.name) Tab")
                }
                Menu { agentMenu(s) } label: {
                    Image(systemName: "chevron.down")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .frame(width: 28, height: 44)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .hoverEffect(.highlight)
                .accessibilityLabel("Quick Launchers")
                .padding(.trailing, 12)
            }
            .disabled(opening.contains(s.id))
            .opacity(opening.contains(s.id) ? 0.5 : 1)
        }
    }

    /// The ▾ menu of a project's launcher cluster: every agent CLI the
    /// host has, its brand tile and name, then how to choose the pins.
    @ViewBuilder private func agentMenu(_ s: Host.Session) -> some View {
        ForEach(host.agents) { agent in
            Button { newProjectTab(s, command: agent.command) } label: {
                // An iOS menu item renders one icon and one plain-text
                // title: no place for a pin check (the sheet shows them).
                Label {
                    Text(agent.name)
                } icon: {
                    ProgramTile(program: .of(agent.id), scale: 1, plate: false)
                        .frame(width: 21, height: 16)
                }
            }
        }
        Divider()
        Button("Configure Pinned…", systemImage: "pin") { configuringLaunchers = s }
    }

    private func newTabRow(_ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Label("New Tab", systemImage: "plus")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, SidebarListMetrics.inset)
                .frame(minHeight: SidebarListMetrics.rowHeight)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .hoverEffect(.highlight)
    }

    private func toggle(_ k: String) {
        withAnimation(reduceMotion ? nil : .snappy) { if expanded.contains(k) { expanded.remove(k) } else { expanded.insert(k) } }
    }

    /// Whether a project shows by the search: by its name, or a tab's title.
    private func matches(_ s: Host.Session) -> Bool {
        query.isEmpty || s.label.localizedCaseInsensitiveContains(query) || (s.view?.windows ?? []).contains(where: filter)
    }

    // MARK: Away

    /// A host away: why, then its projects of the space and its tabs with
    /// no project as last seen, greyed, to read only.
    private func away(_ l: ProjectListing) -> some View {
        let mine = l.projects.filter { $0.space == space }
        let (recent, older) = ProjectOrder.split(mine, used: \.used, name: \.name)
        return VStack(alignment: .leading, spacing: 0) {
            if case .offline = host.status { offline() } else { Text("Connecting…").foregroundStyle(.secondary) }
            ForEach(recent, id: \.id) { awayProject($0) }
            if !older.isEmpty {
                group("older", Text("Older"), count: older.count) {
                    ForEach(older, id: \.id) { awayProject($0) }
                }
            }
            if !l.loose.isEmpty {
                let key = Self.key(host, "loose")
                let isOpen = !collapsed.contains(key)
                Button { toggleLoose(key) } label: {
                    groupRow(Text("No Project"), open: isOpen)
                }
                .buttonStyle(.plain)
                .hoverEffect(.highlight)
                .accessibilityHint(isOpen ? "Collapse" : "Expand")
                if isOpen { awayTabs(l.loose) }
            }
        }
        .saturation(0)
        .opacity(0.6)
        .accessibilityHint("Offline")
    }

    @ViewBuilder private func awayProject(_ project: ProjectListing.Project) -> some View {
        let key = Self.key(host, project.id)
        let isOpen = expanded.contains(key)
        Button { toggle(key) } label: {
            ProjectRow(name: project.name, count: project.tabs.count, open: isOpen)
        }
        .buttonStyle(.plain)
        .accessibilityHint(isOpen ? "Collapse" : "Expand")
        if isOpen { awayTabs(project.tabs).padding(.leading, SidebarListMetrics.indent) }
    }

    private func awayTabs(_ tabs: [ProjectListing.Tab]) -> some View {
        ForEach(tabs, id: \.id) { tab in
            Text(verbatim: tab.title.isEmpty ? " " : tab.title.asText)
                .font(.subheadline)
                .foregroundStyle(.primary.opacity(0.8))
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, SidebarListMetrics.inset)
                .padding(.vertical, 8)
                .frame(minHeight: SidebarListMetrics.rowHeight)
        }
    }

    // MARK: Hosts of before projects

    /// The host's sessions, or why there are none.
    @ViewBuilder private var sessions: some View {
        if host.sessions.isEmpty { Text("No sessions").foregroundStyle(.secondary) }
        ForEach(host.sessions) { s in session(s) }
    }

    @ViewBuilder private func session(_ s: Host.Session) -> some View {
        let windows = entries((s.view?.windows ?? []).map { (s, $0) })
        let isCollapsed = collapsed.contains(s.id)
        // The session's header followed by its tabs.
        VStack(spacing: 4) {
            HStack(spacing: 4) {
                Text(s.label).font(.headline).lineLimit(1)
                Spacer(minLength: 8)
                // Plain ghost buttons: glass is for what floats over the page.
                Button {
                    newWindow(s)
                } label: {
                    Group {
                        if opening.contains(s.id) { ProgressView() } else { Image(systemName: "plus") }
                    }
                    .frame(width: 36, height: 36).contentShape(.circle)
                }
                .disabled(opening.contains(s.id))
                .accessibilityLabel("New Tab")
                Button {
                    withAnimation(.snappy) { if isCollapsed { collapsed.remove(s.id) } else { collapsed.insert(s.id) } }
                } label: {
                    Image(systemName: "chevron.down").rotationEffect(.degrees(isCollapsed ? -90 : 0)).frame(width: 36, height: 36).contentShape(.circle)
                }
                .accessibilityLabel(isCollapsed ? "Expand" : "Collapse")
            }
            .buttonStyle(.plain)
            .font(.body.weight(.semibold))
            .foregroundStyle(.secondary)
            .padding(.leading, 16)
            .padding(.trailing, 6)
            .frame(minHeight: 50)
            .contentShape(.rect)
            .modifier(SessionMenu(host: host, session: s))
            if !isCollapsed && !windows.isEmpty { rows(windows).padding(.leading, SidebarListMetrics.indent) }
        }
    }

    // MARK: Making tabs

    /// Makes a session with a tab, and goes to it.
    private func newSession() {
        creating = true
        Task {
            defer { creating = false }
            do {
                if let (s, b) = try await host.newSession() {
                    open(TerminalTarget(host: host.id, session: s, block: b, title: ""))
                }
            } catch {
                failure = ("Can't Make a New Session", error.localizedDescription)
            }
        }
    }

    /// Opens a tab with no project, and goes to it.
    private func newLooseTab() {
        creating = true
        Task {
            defer { creating = false }
            do {
                if let (s, b) = try await host.newLooseTab() {
                    open(TerminalTarget(host: host.id, session: s, block: b, title: ""))
                }
            } catch {
                failure = ("Can't Open a New Tab", error.localizedDescription)
            }
        }
    }

    /// Opens a tab of a project on its host, in its folder, and goes to
    /// it. A folder gone opens nothing: it offers to close the project.
    private func newProjectTab(_ s: Host.Session, command: [String]? = nil) {
        guard let dir = s.project?.dir else { return newWindow(s) }
        opening.insert(s.id)
        Task {
            defer { opening.remove(s.id) }
            do {
                if host.listsFolders { try await ProjectFolder.ensure(dir) { try await host.folders($0) } }
                if let b = try await host.newWindow(in: s.id, cwd: dir, command: command) {
                    expanded.insert(Self.key(host, s.id))
                    open(TerminalTarget(host: host.id, session: s.id, block: b, title: ""))
                }
            } catch let error where ProjectFolder.missing(error) {
                missing = s
            } catch {
                failure = ("Can't Open a New Tab", error.localizedDescription)
            }
        }
    }

    /// Opens a new tab in a session, and goes to it.
    private func newWindow(_ s: Host.Session) {
        opening.insert(s.id)
        Task {
            defer { opening.remove(s.id) }
            do {
                if let b = try await host.newWindow(in: s.id) {
                    open(TerminalTarget(host: host.id, session: s.id, block: b, title: ""))
                }
            } catch {
                failure = ("Can't Open a New Tab", error.localizedDescription)
            }
        }
    }

    /// Runs a change, and tells why when it fails.
    private func change(_ title: LocalizedStringKey, _ body: @escaping () async throws -> Void) {
        Task {
            do { try await body() } catch { failure = (title, error.localizedDescription) }
        }
    }

    // MARK: Tabs

    /// A tab's menu, as the desktop's tab menu. It moves a tab past the
    /// rows shown, and closes only those: the server's order also has tabs
    /// the list leaves out.
    @ViewBuilder private func menu(_ s: Host.Session, _ w: SessionView.Window, shown: [String]) -> some View {
        let id = w.window_id
        let i = shown.firstIndex(of: id) ?? 0
        // The title changes while the menu is open: it's read on the tap.
        Button("Rename Tab…", systemImage: "pencil") {
            name = w.name ?? ""
            renaming = (s.id, id, host.title(w))
        }
        if w.name != nil {
            Button("Reset Name", systemImage: "arrow.uturn.backward") {
                change("Can't Rename the Tab") { try await host.renameWindow(id, in: s.id, to: "") }
            }
        }
        Divider()
        Button("Move Up", systemImage: "arrow.up") { move(id, in: s, onto: shown[i - 1], shown: shown) }
            .disabled(i <= 0)
        Button("Move Down", systemImage: "arrow.down") { move(id, in: s, onto: shown[i + 1], shown: shown) }
            .disabled(i >= shown.count - 1)
        if host.hasProjects {
            Divider()
            Menu("Move to Project", systemImage: "folder") {
                let targets = host.sessions.filter { $0.project != nil }.sorted { $0.label.localizedStandardCompare($1.label) == .orderedAscending }
                ForEach(targets) { t in
                    Button {
                        change("Can't Move the Tab") { try await host.moveTab(id, to: t.id) }
                    } label: {
                        if t.id == s.id { Label(t.label, systemImage: "checkmark") } else { Text(t.label) }
                    }
                    .disabled(t.id == s.id)
                }
                Divider()
                Button("No Project") {
                    change("Can't Move the Tab") { try await host.moveTab(id, to: "") }
                }
                .disabled(s.project == nil)
            }
        }
        Divider()
        Button("New Tab", systemImage: "plus") {
            if host.hasProjects, s.project != nil { newProjectTab(s) } else { newWindow(s) }
        }
        Button("Close Tab", systemImage: "xmark", role: .destructive) {
            change("Can't Close the Tab") { try await host.closeWindow(id, in: s.id) }
        }
        Button("Close Other Tabs", systemImage: "xmark.square", role: .destructive) {
            change("Can't Close the Tab") {
                for o in shown where o != id { try await host.closeWindow(o, in: s.id) }
            }
        }
        .disabled(shown.count < 2)
    }

    /// Moves a tab of a session to the place of another row shown.
    private func move(_ id: String, in s: Host.Session, onto target: String, shown: [String]) {
        let before = Host.before(id, onto: target, shown: shown, all: (s.view?.windows ?? []).map(\.window_id))
        change("Can't Move the Tab") { [host] in
            do { try await host.moveWindow(id, in: s.id, before: before) } catch {
                // The dropped row goes back to the server's order.
                if drag?.dropped == true { drag = nil }
                throw error
            }
        }
    }

    /// A row's drag: its session's rows as they showed when it lifted, and
    /// the item the system holds until the drag ends. Dropped, it waits for
    /// the server's order.
    private struct Drag {
        let session: Host.Session
        let window: String
        let from: [String]
        let token = UUID()
        weak var item: Lifted?
        var dropped = false
    }

    /// Lifts a row, from the order the rows show.
    private func lift(_ w: Entry, shown: [String]) -> NSItemProvider {
        // The system asks again as the rows move: the same drag goes on.
        if let d = drag, !d.dropped, d.window == w.window.window_id, let item = d.item { return item }
        let item = Lifted(object: w.id as NSString)
        var d = Drag(session: w.session, window: w.window.window_id, from: shown)
        item.token = d.token
        item.mark(d.token)
        d.item = item
        drag = d
        order = shown
        lifts += 1
        return item
    }

    /// Makes way for the dragged row at another row of its session.
    private func pass(over w: Entry) {
        guard let d = drag, !d.dropped, d.session.id == w.session.id,
              let from = order.firstIndex(of: d.window), let to = order.firstIndex(of: w.window.window_id), from != to
        else { return }
        withAnimation(.snappy) { order.move(fromOffsets: [from], toOffset: to > from ? to + 1 : to) }
    }

    /// Moves the dropped row on the server to where the drag left it.
    private func drop(_ items: [NSItemProvider]) -> Bool {
        guard var d = drag, !d.dropped else {
            // Another card's row, which cannot move here.
            PageDrop.missed(items)
            return false
        }
        guard let i = order.firstIndex(of: d.window), let j = d.from.firstIndex(of: d.window), i != j else {
            drag = nil
            return true
        }
        d.dropped = true
        drag = d
        move(d.window, in: d.session, onto: d.from[i], shown: d.from)
        // Should the server's order not change, the rows go back to it.
        Task {
            try? await Task.sleep(for: .seconds(2))
            if drag?.token == d.token { drag = nil }
        }
        return true
    }

    /// A card's rows in the order a drag of one of them leaves.
    private func arranged(_ windows: [Entry]) -> [Entry] {
        guard let d = drag, windows.allSatisfy({ $0.session.id == d.session.id }) else { return windows }
        return windows.sorted { (order.firstIndex(of: $0.window.window_id) ?? .max) < (order.firstIndex(of: $1.window.window_id) ?? .max) }
    }

    /// A tab's row in a card.
    private struct Entry {
        let session: Host.Session
        let block: String
        let window: SessionView.Window
        var id: String { session.id + "/" + window.window_id }
    }

    /// Whether a tab's row shows, by the search. The title is read only
    /// while searching: read, its change would redraw the host's rows.
    private func filter(_ w: SessionView.Window) -> Bool {
        query.isEmpty || host.title(w).localizedCaseInsensitiveContains(query)
    }

    /// The rows the search keeps.
    private func entries(_ windows: [(Host.Session, SessionView.Window)]) -> [Entry] {
        windows.compactMap { s, w in
            w.block.flatMap { filter(w) ? Entry(session: s, block: $0, window: w) : nil }
        }
    }

    /// The rows, with each tab's menu and drag targets.
    private func rows(_ entries: [Entry]) -> some View {
        let windows = arranged(entries)
        let shownBy = RowGroups.shown(windows.map { ($0.session.id, $0.window.window_id) })
        return ForEach(windows, id: \.id) { w in
            let selected = current.map { $0.host == host.id && $0.session == w.session.id && w.window.holds($0.block) } ?? false
            let row = Button {
                openRow(TerminalTarget(host: host.id, session: w.session.id, block: w.block, title: host.title(w.window)))
            } label: {
                WindowRow(host: host, block: w.block, window: w.window, tiles: 0,
                          divider: false, compact: true) {
                    if !showsTerminalAside {
                        Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                    }
                }
            }
            .buttonStyle(.plain)
            .hoverEffect(.highlight)
            .background {
                if selected {
                    RoundedRectangle(cornerRadius: SidebarListMetrics.selectionRadius)
                        .fill(Color.primary.opacity(0.12))
                        .padding(.vertical, 2)
                }
            }
            let shown = shownBy[w.session.id] ?? []
            let place = RowPlace(host: host.id, session: w.session.id, window: w.window.window_id)
            let lifted = drag.map { !$0.dropped && $0.session.id == w.session.id && $0.window == w.window.window_id } ?? false
            row.onDrag { lift(w, shown: shown) }
                .contextMenu { menu(w.session, w.window, shown: shown) }
                .onDrop(of: [.text], delegate: RowDrop(
                    pass: { hostDrop?.active() == true ? hostDrop?.pass() ?? () : pass(over: w) },
                    drop: { hostDrop?.active() == true ? hostDrop?.drop($0) ?? false : drop($0) }))
                .opacity(lifted ? 0.4 : 1)
                .modifier(MatchedPlace(place: place, own: .tab(place), namespace: places))
                .accessibilityAddTraits(selected ? .isSelected : [])
                .accessibilityValue(Text("Current"), isEnabled: selected)
        }
    }
}

enum SidebarListMetrics {
    static let rowHeight: CGFloat = 44
    static let inset: CGFloat = 10
    static let gap: CGFloat = 8
    static let folderWidth: CGFloat = 18
    static let indent: CGFloat = 32
    static let selectionRadius: CGFloat = 8
}

/// A project row with the same outline icons as the desktop.
private struct ProjectRow: View {
    let name: String
    let count: Int
    let open: Bool
    var busy = false

    var body: some View {
        HStack(spacing: SidebarListMetrics.gap) {
            Image(open && count > 0 ? "SidebarFolderOpen" : "SidebarFolderClosed")
                .resizable()
                .scaledToFit()
                .foregroundStyle(.secondary)
                .frame(width: SidebarListMetrics.folderWidth, height: SidebarListMetrics.folderWidth)
                .accessibilityHidden(true)
            Text(verbatim: name).font(.subheadline.weight(.semibold)).lineLimit(1)
            Spacer(minLength: 8)
            if busy { ProgressView() }
            if count > 0 { Chevron(open: open) }
        }
        .padding(.horizontal, SidebarListMetrics.inset)
        .padding(.vertical, 8)
        .frame(minHeight: SidebarListMetrics.rowHeight)
        .contentShape(.rect)
    }
}

/// A row's chevron: right when folded, down when open.
private struct Chevron: View {
    let open: Bool

    var body: some View {
        Image(systemName: "chevron.right")
            .font(.footnote.weight(.semibold))
            .foregroundStyle(.gray)
            .rotationEffect(.degrees(open ? 90 : 0))
    }
}

/// The list's hand for a host's drag into a section: whether one is on,
/// and where it passes and lands. A host's drag leaves the rows' targets
/// to it, so a drop anywhere in the section reorders the hosts.
struct HostDrop {
    let active: () -> Bool
    let pass: () -> Void
    let drop: ([NSItemProvider]) -> Bool
}

/// The header's drag source, when the host can move: with no lift the
/// header starts no drag, so a long press answers as before.
private struct HostLift: ViewModifier {
    let lift: (() -> NSItemProvider)?

    func body(content: Content) -> some View {
        if let lift {
            content.onDrag(lift)
        } else {
            content
        }
    }
}

/// A session's menu: rename it, or close it after a yes. On the header,
/// for the question to point at the session.
private struct SessionMenu: ViewModifier {
    let host: Host
    let session: Host.Session
    @State private var renaming = false
    @State private var closing = false
    @State private var name = ""
    /// What failed, and why, as the server says it.
    @State private var failure: (title: LocalizedStringKey, why: String)?

    func body(content: Content) -> some View {
        content
            .contextMenu {
                Button("Rename Session…", systemImage: "pencil") {
                    name = session.label
                    renaming = true
                }
                Button("Close Session", systemImage: "xmark", role: .destructive) { closing = true }
            }
            .confirmationDialog("Close Session", isPresented: $closing) {
                Button("Close Session", role: .destructive) {
                    change("Can't Close the Session") { [host, session] in try await host.closeSession(session.id) }
                }
            } message: {
                Text("Close \(session.label) and every tab in it?")
            }
            .alert("Rename Session", isPresented: $renaming) {
                TextField(session.label, text: $name)
                Button("Cancel", role: .cancel) {}
                Button("Save") {
                    change("Can't Rename the Session") { [host, session, name] in try await host.renameSession(session.id, to: name) }
                }
            }
            .alert(failure?.title ?? "", isPresented: Binding { failure != nil } set: { if !$0 { failure = nil } }) {
            } message: {
                Text(failure?.why ?? "")
            }
    }

    private func change(_ title: LocalizedStringKey, _ body: @escaping () async throws -> Void) {
        Task {
            do { try await body() } catch { failure = (title, error.localizedDescription) }
        }
    }
}

/// Sheet for configuring which installed agent CLIs are pinned in a project's
/// New Tab row (docs/MOD-010-agent-launchers.md).
private struct ProjectLaunchersSheet: View {
    let host: Host
    let sessionID: String
    let projectName: String
    let onError: (String) -> Void
    @Environment(\.dismiss) private var dismiss

    private var currentLaunchers: [String]? {
        host.sessions.first(where: { $0.id == sessionID })?.project?.launchers
    }

    private func isPinned(_ id: String) -> Bool {
        guard let pinned = currentLaunchers else { return true }
        return pinned.contains(id)
    }

    private func setPinned(_ id: String, on: Bool) {
        var next = currentLaunchers ?? host.agents.map(\.id)
        if on {
            if !next.contains(id) {
                next = host.agents.map(\.id).filter { $0 == id || next.contains($0) }
            }
        } else {
            next.removeAll { $0 == id }
        }
        let value: [String]? = (next == host.agents.map(\.id)) ? nil : next
        Task {
            do { try await host.updateProject(sessionID, launchers: .some(value)) } catch { onError(error.localizedDescription) }
        }
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    ForEach(host.agents) { agent in
                        Toggle(isOn: Binding(get: { isPinned(agent.id) }, set: { setPinned(agent.id, on: $0) })) {
                            HStack(spacing: 12) {
                                ProgramTile(program: .of(agent.id), plate: false)
                                    .frame(width: 21 * ProgramTiles.scale, height: 16 * ProgramTiles.scale)
                                Text(agent.name)
                            }
                        }
                    }
                } header: {
                    Text(projectName)
                } footer: {
                    Text("Pinned agent CLIs appear in this project's quick-launcher row. Turn all off to show New Tab.")
                }
                if currentLaunchers != nil {
                    Section {
                        Button("Show All Installed") {
                            Task {
                                do { try await host.updateProject(sessionID, launchers: .some(nil)) } catch { onError(error.localizedDescription) }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Quick Launchers")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}
