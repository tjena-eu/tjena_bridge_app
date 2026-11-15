import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:tjena_bridge/screens/move_screen.dart';

void main() {
  test('move code round trip', () {
    final c = MoveCode.create('https://wap.tjena.eu');
    final p = MoveCode.parse(c.uri)!;
    expect(p.id, c.id);
    expect(p.key, c.key);
    expect(p.gateway, 'https://wap.tjena.eu');
    expect(base64Url.decode(base64Url.normalize(p.key)).length, 32);
    expect(RegExp(r'^[a-zA-Z0-9_-]{16,64}$').hasMatch(p.id), isTrue); // gateway's id check
    expect(p.wsUri('send').toString(), startsWith('wss://wap.tjena.eu/_tjena/move/v1/send?id='));
  });

  test('without a code: no id in the address', () {
    final c = MoveCode.withoutCode('https://wap.tjena.eu/');
    expect(c.withoutCode, isTrue);
    expect(c.wsUri('receive').toString(), 'wss://wap.tjena.eu/_tjena/move/v1/receive');
  });

  test('other codes are refused', () {
    expect(MoveCode.parse('tjena-bridge://login?hs=https://a&gw=https://b&token=x'), isNull);
    expect(MoveCode.parse('tjena-bridge://move?id=abc'), isNull);
    expect(MoveCode.parse('tjena-bridge://move?id=abc&key=k&gw=javascript:alert(1)'), isNull);
    expect(MoveCode.parse('https://example.com'), isNull);
  });
}
