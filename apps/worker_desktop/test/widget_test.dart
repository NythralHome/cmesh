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
    expect(find.text('Worker status'), findsOneWidget);
    expect(find.text('Status unknown'), findsOneWidget);
    expect(find.text('Invite required'), findsOneWidget);
    expect(find.widgetWithText(FilledButton, 'Open invite'), findsOneWidget);
    expect(find.widgetWithText(FilledButton, 'Connect & start'), findsNothing);
  });

  testWidgets('requires saving invite settings before start', (tester) async {
    await tester.pumpWidget(
      const CMeshWorkerApp(
        initialInvite: null,
        autostartControl: false,
        registerProtocolHandler: false,
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Connection'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.widgetWithText(TextFormField, 'Join token'),
      'test-token',
    );
    await tester.pumpAndSettle();

    expect(find.text('Save settings first'), findsOneWidget);
    final start = find.widgetWithText(FilledButton, 'Connect & start');
    expect(start, findsOneWidget);
    expect(tester.widget<FilledButton>(start).onPressed, isNull);
  });

  test('parses invite URLs', () {
    final invite = InviteConfig.fromString(
      'cmesh://join?manager=https%3A%2F%2Fcmesh.example.com&token=abc123',
    );

    expect(invite?.managerUrl, 'https://cmesh.example.com');
    expect(invite?.joinToken, 'abc123');
  });

  test('parses worker runtime status', () {
    final status = WorkerRuntimeStatus.fromJson({
      'running': true,
      'pid': 4120,
      'started_at': '2026-06-15T05:30:00Z',
      'log_tail': 'started worker pid=4120\n',
    });

    expect(status.running, isTrue);
    expect(status.pid, 4120);
    expect(status.label, 'Running');
    expect(
      status.startedAt?.toUtc().toIso8601String(),
      '2026-06-15T05:30:00.000Z',
    );
  });
}
