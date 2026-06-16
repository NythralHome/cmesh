import Cocoa
import FlutterMacOS

final class MacStatusItemBridge {
  private static var statusItem: NSStatusItem?

  static func configure(statusItem: NSStatusItem) {
    self.statusItem = statusItem
  }

  static func configure(controller: FlutterViewController) {
    let channel = FlutterMethodChannel(
      name: "cmesh.worker_desktop/status_item",
      binaryMessenger: controller.engine.binaryMessenger)

    channel.setMethodCallHandler { call, result in
      switch call.method {
      case "configure":
        update(running: false, label: "Not running")
        result(nil)
      case "update":
        let args = call.arguments as? [String: Any]
        let running = args?["running"] as? Bool ?? false
        let label = args?["label"] as? String ?? "Not running"
        update(running: running, label: label)
        result(nil)
      default:
        result(FlutterMethodNotImplemented)
      }
    }
  }

  private static func update(running: Bool, label: String) {
    DispatchQueue.main.async {
      guard let button = statusItem?.button else { return }
      button.title = ""
      button.toolTip = "CMesh Worker: \(label)"
      button.image = AppDelegate.statusImage(running: running)
      button.imagePosition = .imageOnly
    }
  }
}
