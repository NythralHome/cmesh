import Cocoa
import FlutterMacOS

@main
class AppDelegate: FlutterAppDelegate {
  private var statusItem: NSStatusItem?
  private var statusMenu: NSMenu?

  override func applicationDidFinishLaunching(_ notification: Notification) {
    super.applicationDidFinishLaunching(notification)
    configureStatusItem()
  }

  override func application(_ application: NSApplication, open urls: [URL]) {
    for url in urls {
      InviteURLBridge.handle(url: url)
    }
  }

  override func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    return false
  }

  override func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool {
    return true
  }

  private func configureStatusItem() {
    let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    item.button?.title = "CMesh"
    if #available(macOS 11.0, *) {
      item.button?.image = NSImage(systemSymbolName: "point.3.connected.trianglepath.dotted", accessibilityDescription: "CMesh")
    }
    item.button?.imagePosition = .imageLeading

    let menu = NSMenu()
    menu.addItem(NSMenuItem(title: "Show CMesh Worker", action: #selector(showMainWindow), keyEquivalent: ""))
    menu.addItem(NSMenuItem.separator())
    menu.addItem(NSMenuItem(title: "Quit", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q"))
    item.menu = menu

    statusItem = item
    statusMenu = menu
    MacStatusItemBridge.configure(statusItem: item)
  }

  @objc private func showMainWindow() {
    NSApp.activate(ignoringOtherApps: true)
    if let window = NSApp.windows.first {
      window.makeKeyAndOrderFront(nil)
    }
  }
}
