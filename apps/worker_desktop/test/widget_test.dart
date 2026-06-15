import 'package:cmesh_worker_desktop/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  testWidgets('renders worker connection controls', (tester) async {
    await tester.pumpWidget(
      const CMeshWorkerApp(
        initialInvite: null,
        autostartControl: false,
        registerProtocolHandler: false,
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('CMesh Worker'), findsOneWidget);
    expect(find.text('Connection'), findsOneWidget);
    expect(find.text('Worker control'), findsOneWidget);
    expect(find.widgetWithText(FilledButton, 'Connect worker'), findsOneWidget);
  });

  test('parses invite URLs', () {
    final invite = InviteConfig.fromString(
      'cmesh://join?manager=https%3A%2F%2Fcmesh.example.com&token=abc123',
    );

    expect(invite?.managerUrl, 'https://cmesh.example.com');
    expect(invite?.joinToken, 'abc123');
  });
}
