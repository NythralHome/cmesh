import Cocoa
import FlutterMacOS

@main
class AppDelegate: FlutterAppDelegate {
  private var statusItem: NSStatusItem?
  private var statusMenu: NSMenu?
  private var openedFromInviteURL = false

  override func applicationDidFinishLaunching(_ notification: Notification) {
    super.applicationDidFinishLaunching(notification)
    NSApp.setActivationPolicy(.accessory)
    configureStatusItem()
    DispatchQueue.main.asyncAfter(deadline: .now() + 0.35) {
      if !self.openedFromInviteURL {
        self.hideMainWindow()
      }
    }
  }

  override func application(_ application: NSApplication, open urls: [URL]) {
    openedFromInviteURL = true
    for url in urls {
      InviteURLBridge.handle(url: url)
    }
    showMainWindow()
  }

  override func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    return false
  }

  override func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool {
    return true
  }

  private func configureStatusItem() {
    let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    item.button?.title = "CM"
    item.button?.image = Self.statusImage(running: false)
    item.button?.imagePosition = .imageLeading
    item.button?.toolTip = "CMesh Worker: Not running"

    let menu = NSMenu()
    menu.addItem(NSMenuItem(title: "Show CMesh Worker", action: #selector(showMainWindow), keyEquivalent: ""))
    menu.addItem(NSMenuItem.separator())
    menu.addItem(NSMenuItem(title: "Quit", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q"))
    item.menu = menu

    statusItem = item
    statusMenu = menu
    MacStatusItemBridge.configure(statusItem: item)
  }

  static func statusImage(running: Bool) -> NSImage {
    let image = NSImage(size: NSSize(width: 18, height: 18))
    image.lockFocus()

    let strokeColor = NSColor.labelColor
    strokeColor.setStroke()
    strokeColor.setFill()

    let lineWidth: CGFloat = 1.8
    let center = NSPoint(x: 9, y: 9)
    let nodes = [
      NSPoint(x: 9, y: 15),
      NSPoint(x: 15, y: 9),
      NSPoint(x: 9, y: 3),
      NSPoint(x: 3, y: 9),
    ]

    for node in nodes {
      let path = NSBezierPath()
      path.move(to: center)
      path.line(to: node)
      path.lineWidth = lineWidth
      path.lineCapStyle = .round
      path.stroke()
    }

    let centerDot = NSBezierPath(ovalIn: NSRect(x: 6.5, y: 6.5, width: 5, height: 5))
    centerDot.fill()

    for node in nodes {
      let dot = NSBezierPath(ovalIn: NSRect(x: node.x - 2.2, y: node.y - 2.2, width: 4.4, height: 4.4))
      dot.lineWidth = lineWidth
      dot.stroke()
      if running {
        dot.fill()
      }
    }

    image.unlockFocus()
    image.isTemplate = true
    return image
  }

  @objc private func showMainWindow() {
    NSApp.activate(ignoringOtherApps: true)
    if let window = NSApp.windows.first {
      if window.isMiniaturized {
        window.deminiaturize(nil)
      }
      window.makeKeyAndOrderFront(nil)
      window.orderFrontRegardless()
    }
  }

  private func hideMainWindow() {
    if let window = NSApp.windows.first {
      window.orderOut(nil)
    }
  }
}
