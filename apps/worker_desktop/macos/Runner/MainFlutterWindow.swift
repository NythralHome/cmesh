import Cocoa
import FlutterMacOS

class MainFlutterWindow: NSWindow {
  override func awakeFromNib() {
    let flutterViewController = FlutterViewController()
    let windowFrame = self.frame
    self.contentViewController = flutterViewController
    self.setFrame(windowFrame, display: true)
    self.titlebarAppearsTransparent = false
    self.isMovableByWindowBackground = true
    self.backgroundColor = NSColor.windowBackgroundColor
    self.isOpaque = true

    InviteURLBridge.configure(controller: flutterViewController)
    MacStatusItemBridge.configure(controller: flutterViewController)
    RegisterGeneratedPlugins(registry: flutterViewController)

    super.awakeFromNib()
  }
}
