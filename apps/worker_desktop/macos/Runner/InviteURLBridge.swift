import Cocoa
import FlutterMacOS

final class InviteURLBridge {
  private static let channelName = "cmesh.worker_desktop/invite"
  private static var channel: FlutterMethodChannel?
  private static var pendingURL: String?

  static func configure(controller: FlutterViewController) {
    channel = FlutterMethodChannel(
      name: channelName,
      binaryMessenger: controller.engine.binaryMessenger)
    channel?.setMethodCallHandler { call, result in
      if call.method == "getInitialInvite" {
        result(pendingURL)
        pendingURL = nil
        return
      }
      result(FlutterMethodNotImplemented)
    }

    if let pendingURL = pendingURL {
      channel?.invokeMethod("openInvite", arguments: pendingURL)
      self.pendingURL = nil
    }
  }

  static func handle(url: URL) {
    let rawURL = url.absoluteString
    guard rawURL.hasPrefix("cmesh://") else {
      return
    }
    if let channel = channel {
      channel.invokeMethod("openInvite", arguments: rawURL)
    } else {
      pendingURL = rawURL
    }
  }
}
