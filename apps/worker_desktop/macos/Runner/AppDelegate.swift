import Cocoa
import FlutterMacOS

@main
class AppDelegate: FlutterAppDelegate {
  private var openedFromInviteURL = false

  override func applicationDidFinishLaunching(_ notification: Notification) {
    super.applicationDidFinishLaunching(notification)
    NSApp.setActivationPolicy(.accessory)
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
