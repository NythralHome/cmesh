import 'package:cmesh_worker_desktop/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  testWidgets('renders worker connection controls', (tester) async {
    await tester.pumpWidget(const CMeshWorkerApp());
    await tester.pumpAndSettle();

    expect(find.text('CMesh Worker'), findsOneWidget);
    expect(find.text('Connection'), findsOneWidget);
    expect(find.text('Worker control'), findsOneWidget);
    expect(find.widgetWithText(FilledButton, 'Connect worker'), findsOneWidget);
  });
}
