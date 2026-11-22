// StubApp.swift is a stand-in for the desktop app, used only by the e2e
// CI job (internal/e2e). It is built with swiftc into a bundle with a test
// bundle id (scripts/ci/build-stubapp.sh); it is never given the real
// app's bundle id and refuses to start with it.
//
// What it does:
//   - At launch it creates its data folder, appends a line to
//     <data_dir>/stubapp-runs.txt and keeps that file open (an open file
//     under the folder, as the real app has), and holds an fcntl write lock
//     on <data_dir>/Local Storage/leveldb/LOCK when that directory exists.
//   - It appends main.log lines in the formats of
//     internal/compat/manifests/6.x.toml [signals] to <log_dir>/main.log:
//     app started, the active account and organization (read from the
//     data folder's config.json and session directories), the focused
//     session, an unclean previous exit, a vetoed quit and a turn end.
//   - It handles the quit Apple Event through NSApplication. With a veto
//     of N seconds it logs the quit-vetoed line and quits N seconds later:
//     veto_mode "later" answers the quit event after N seconds
//     (terminateLater); veto_mode "cancel" refuses the event and quits by
//     itself after N seconds.
//   - It records launch, quit-request and exit events in
//     <state_dir>/stubapp-events.txt and its pid in <state_dir>/stubapp.pid.
//
// Settings, first match wins: command-line arguments, environment, then
// the file "stubapp.conf" next to the bundle (or STUBAPP_CONFIG). The file
// holds key = value lines; "#" starts a comment line.
//
//   key            argument              environment
//   data_dir       --user-data-dir PATH  STUBAPP_DATA_DIR
//   log_dir                              STUBAPP_LOG_DIR
//   state_dir                            STUBAPP_STATE_DIR (default log_dir)
//   veto_seconds   --veto-quit-seconds N STUBAPP_VETO_SECONDS (default 0)
//   veto_mode                            STUBAPP_VETO_MODE (later or cancel)
//   session                              STUBAPP_SESSION (a fake local_ id)
//
// The veto settings are read again at every quit request, so a test can
// change them while the stub runs. --print-config prints the resolved
// settings and exits without starting the app.
//
// The stub refuses to start when data_dir or log_dir is missing or lies
// under the real user's Library/Application Support/Claude* or
// Library/Logs/Claude folders, or when its bundle id belongs to the real
// app.

import AppKit
import Darwin

let realBundleID = "com.anthropic.claudefordesktop"
let confFileName = "stubapp.conf"

struct StubConfig {
    var confPath = ""
    var dataDir = ""
    var logDir = ""
    var stateDir = ""
    var vetoSeconds = 0.0
    var vetoMode = "later"
    var session = ""
}

// readConfFile parses key = value lines. A missing file is empty.
func readConfFile(_ path: String) -> [String: String] {
    guard !path.isEmpty, let text = try? String(contentsOfFile: path, encoding: .utf8) else {
        return [:]
    }
    var out: [String: String] = [:]
    for raw in text.split(whereSeparator: { $0.isNewline }) {
        let line = raw.trimmingCharacters(in: .whitespaces)
        if line.isEmpty || line.hasPrefix("#") {
            continue
        }
        guard let eq = line.firstIndex(of: "=") else {
            continue
        }
        let key = line[..<eq].trimmingCharacters(in: .whitespaces)
        let value = line[line.index(after: eq)...].trimmingCharacters(in: .whitespaces)
        out[key] = value
    }
    return out
}

// argValue returns the value of "--name value" or "--name=value".
func argValue(_ name: String) -> String? {
    let args = CommandLine.arguments
    var i = 1
    while i < args.count {
        let a = args[i]
        if a == name && i + 1 < args.count {
            return args[i + 1]
        }
        if a.hasPrefix(name + "=") {
            return String(a.dropFirst(name.count + 1))
        }
        i += 1
    }
    return nil
}

func defaultConfPath() -> String {
    let bundle = Bundle.main.bundlePath as NSString
    return (bundle.deletingLastPathComponent as NSString).appendingPathComponent(confFileName)
}

func loadConfig() -> StubConfig {
    let env = ProcessInfo.processInfo.environment
    var c = StubConfig()
    c.confPath = env["STUBAPP_CONFIG"].flatMap { $0.isEmpty ? nil : $0 } ?? defaultConfPath()
    let file = readConfFile(c.confPath)
    func pick(_ arg: String?, _ envKey: String, _ fileKey: String) -> String? {
        if let a = arg, !a.isEmpty {
            return a
        }
        if let e = env[envKey], !e.isEmpty {
            return e
        }
        if let f = file[fileKey], !f.isEmpty {
            return f
        }
        return nil
    }
    c.dataDir = pick(argValue("--user-data-dir"), "STUBAPP_DATA_DIR", "data_dir") ?? ""
    c.logDir = pick(nil, "STUBAPP_LOG_DIR", "log_dir") ?? ""
    c.stateDir = pick(nil, "STUBAPP_STATE_DIR", "state_dir") ?? c.logDir
    if let v = pick(argValue("--veto-quit-seconds"), "STUBAPP_VETO_SECONDS", "veto_seconds"),
       let d = Double(v), d >= 0 {
        c.vetoSeconds = d
    }
    c.vetoMode = pick(nil, "STUBAPP_VETO_MODE", "veto_mode") ?? "later"
    c.session = pick(nil, "STUBAPP_SESSION", "session") ?? ""
    return c
}

// realHome is the home directory from the user database, not $HOME.
func realHome() -> String? {
    guard let pw = getpwuid(getuid()), let dir = pw.pointee.pw_dir else {
        return nil
    }
    let s = String(cString: dir)
    return s.isEmpty ? nil : s
}

func normalized(_ p: String) -> String {
    return ((p as NSString).standardizingPath as NSString).resolvingSymlinksInPath
}

// safetyProblem returns why the stub must not start, or nil.
func safetyProblem(_ c: StubConfig) -> String? {
    if let id = Bundle.main.bundleIdentifier {
        if id == realBundleID || id.lowercased().hasPrefix("com.anthropic.") {
            return "the bundle id belongs to the real app"
        }
    }
    for (name, p) in [("data_dir", c.dataDir), ("log_dir", c.logDir), ("state_dir", c.stateDir)] {
        if p.isEmpty || !p.hasPrefix("/") {
            return "\(name) must be an absolute path"
        }
    }
    if c.vetoMode != "later" && c.vetoMode != "cancel" {
        return "veto_mode must be later or cancel"
    }
    if c.session != "" && !c.session.hasPrefix("local_") {
        return "session must be a local_ session id"
    }
    if let home = realHome() {
        let h = normalized(home)
        let banned = [h + "/Library/Application Support/Claude", h + "/Library/Logs/Claude"]
        for p in [c.dataDir, c.logDir, c.stateDir] {
            let n = normalized(p)
            for b in banned where n.hasPrefix(b) {
                return "a configured path lies under the real app's folders"
            }
        }
    }
    return nil
}

// isUUID accepts the lower-case 8-4-4-4-12 form.
func isUUID(_ s: String) -> Bool {
    let chars = Array(s.utf8)
    if chars.count != 36 {
        return false
    }
    for (i, c) in chars.enumerated() {
        if i == 8 || i == 13 || i == 18 || i == 23 {
            if c != UInt8(ascii: "-") {
                return false
            }
        } else if !((c >= UInt8(ascii: "0") && c <= UInt8(ascii: "9")) || (c >= UInt8(ascii: "a") && c <= UInt8(ascii: "f"))) {
            return false
        }
    }
    return true
}

// activeReplica returns the account recorded in the data folder's
// config.json and the first organization directory of its sessions, as
// the real app reports them at startup. Fixtures hold fake ids only.
func activeReplica(_ dataDir: String) -> (String, String)? {
    let cfg = (dataDir as NSString).appendingPathComponent("config.json")
    guard let data = FileManager.default.contents(atPath: cfg),
          let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
          let account = obj["lastKnownAccountUuid"] as? String, isUUID(account) else {
        return nil
    }
    let dir = (dataDir as NSString).appendingPathComponent("claude-code-sessions/" + account)
    guard let orgs = try? FileManager.default.contentsOfDirectory(atPath: dir),
          let org = orgs.filter(isUUID).sorted().first else {
        return nil
    }
    return (account, org)
}

// appendLine appends text to path, creating it with mode 0644.
func appendLine(_ path: String, _ text: String) {
    let fd = open(path, O_WRONLY | O_APPEND | O_CREAT | O_CLOEXEC, 0o644)
    if fd < 0 {
        return
    }
    defer { close(fd) }
    let bytes = Array((text + "\n").utf8)
    _ = bytes.withUnsafeBufferPointer { write(fd, $0.baseAddress, $0.count) }
}

func makeDirs(_ path: String) {
    try? FileManager.default.createDirectory(atPath: path, withIntermediateDirectories: true)
}

let stampFormatter: DateFormatter = {
    let f = DateFormatter()
    f.locale = Locale(identifier: "en_US_POSIX")
    f.dateFormat = "yyyy-MM-dd HH:mm:ss.SSS"
    return f
}()

@MainActor
final class StubDelegate: NSObject, NSApplicationDelegate {
    let config: StubConfig
    let pid = getpid()
    var holdFD: Int32 = -1
    var lockFD: Int32 = -1
    var quitAllowed = false
    var vetoTimer: Timer?

    init(config: StubConfig) {
        self.config = config
    }

    var pidPath: String { (config.stateDir as NSString).appendingPathComponent("stubapp.pid") }
    var eventsPath: String { (config.stateDir as NSString).appendingPathComponent("stubapp-events.txt") }
    var logPath: String { (config.logDir as NSString).appendingPathComponent("main.log") }

    func mainLog(_ message: String) {
        appendLine(logPath, "[\(stampFormatter.string(from: Date()))] [info] \(message)")
    }

    func event(_ message: String) {
        appendLine(eventsPath, "\(message) pid=\(pid)")
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        makeDirs(config.dataDir)
        makeDirs(config.logDir)
        makeDirs(config.stateDir)
        if FileManager.default.fileExists(atPath: pidPath) {
            mainLog("Previous run did not exit cleanly")
        }
        appendLine(pidPath + ".tmp", "\(pid)")
        _ = rename(pidPath + ".tmp", pidPath)

        let runs = (config.dataDir as NSString).appendingPathComponent("stubapp-runs.txt")
        holdFD = open(runs, O_WRONLY | O_APPEND | O_CREAT | O_CLOEXEC, 0o644)
        if holdFD >= 0 {
            let bytes = Array("launch pid=\(pid)\n".utf8)
            _ = bytes.withUnsafeBufferPointer { write(holdFD, $0.baseAddress, $0.count) }
        }
        lockLevelDB()

        mainLog("stubapp: launched")
        mainLog("[startup-perf] main window shown 120 ms after launch")
        if let (account, org) = activeReplica(config.dataDir) {
            mainLog("[ScheduledTasks] Initialized { accountId: '\(account)', orgId: '\(org)' }")
        }
        if !config.session.isEmpty {
            mainLog("setFocusedSession: sessionId=\(config.session), source=stub")
        }
        event("launch data_dir=\(config.dataDir)")
    }

    // lockLevelDB takes an fcntl write lock on the Local Storage LOCK file,
    // as the real app does while it runs.
    func lockLevelDB() {
        let dir = (config.dataDir as NSString).appendingPathComponent("Local Storage/leveldb")
        var isDir: ObjCBool = false
        guard FileManager.default.fileExists(atPath: dir, isDirectory: &isDir), isDir.boolValue else {
            return
        }
        let path = (dir as NSString).appendingPathComponent("LOCK")
        lockFD = open(path, O_RDWR | O_CREAT | O_CLOEXEC, 0o644)
        if lockFD < 0 {
            event("lock-open-failed")
            return
        }
        var fl = flock()
        fl.l_type = Int16(F_WRLCK)
        fl.l_whence = Int16(SEEK_SET)
        fl.l_start = 0
        fl.l_len = 0
        if fcntl(lockFD, F_SETLK, &fl) != 0 {
            event("lock-busy")
        }
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if quitAllowed {
            return .terminateNow
        }
        // Read the veto settings again: a test may change them while the
        // stub runs.
        let c = loadConfig()
        event("quit-request veto=\(Int(c.vetoSeconds)) mode=\(c.vetoMode)")
        if c.vetoSeconds <= 0 {
            return .terminateNow
        }
        mainLog("beforeQuit: vetoed by before-quit interceptor")
        if vetoTimer != nil {
            // A veto is already running; its timer decides.
            return c.vetoMode == "cancel" ? .terminateCancel : .terminateLater
        }
        let later = c.vetoMode == "later"
        let session = c.session
        let timer = Timer(timeInterval: c.vetoSeconds, repeats: false) { _ in
            MainActor.assumeIsolated {
                self.vetoEnded(later: later, session: session)
            }
        }
        // terminateLater runs the loop in the modal panel mode until the
        // reply; schedule the timer there too.
        RunLoop.main.add(timer, forMode: .common)
        RunLoop.main.add(timer, forMode: .modalPanel)
        vetoTimer = timer
        return later ? .terminateLater : .terminateCancel
    }

    func vetoEnded(later: Bool, session: String) {
        vetoTimer = nil
        if !session.isEmpty {
            mainLog("[Stop hook] Query completed for session \(session)")
        }
        event("veto-ended")
        quitAllowed = true
        if later {
            NSApp.reply(toApplicationShouldTerminate: true)
        } else {
            NSApp.terminate(nil)
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        event("exit")
        if holdFD >= 0 {
            close(holdFD)
        }
        if lockFD >= 0 {
            close(lockFD)
        }
        _ = unlink(pidPath)
    }
}

let startConfig = loadConfig()

if CommandLine.arguments.contains("--print-config") {
    print("bundle_id=\(Bundle.main.bundleIdentifier ?? "")")
    print("conf=\(startConfig.confPath)")
    print("data_dir=\(startConfig.dataDir)")
    print("log_dir=\(startConfig.logDir)")
    print("state_dir=\(startConfig.stateDir)")
    print("veto_seconds=\(startConfig.vetoSeconds)")
    print("veto_mode=\(startConfig.vetoMode)")
    print("session=\(startConfig.session)")
    if let problem = safetyProblem(startConfig) {
        print("refused: \(problem)")
        exit(2)
    }
    exit(0)
}

if let problem = safetyProblem(startConfig) {
    FileHandle.standardError.write(Data("stubapp: refusing to start: \(problem)\n".utf8))
    exit(2)
}

MainActor.assumeIsolated {
    let app = NSApplication.shared
    let delegate = StubDelegate(config: startConfig)
    app.delegate = delegate
    app.setActivationPolicy(.accessory)
    app.run()
}
