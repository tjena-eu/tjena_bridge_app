import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:pretty_qr_code/pretty_qr_code.dart';

import '../matrix_api.dart';
import '../native.dart';
import 'widgets.dart';

// Moving the bridge to a new phone.
//
// The old phone shows a QR code with a one-time id and a random key. The new
// phone (signed in with the same tjena account) scans it. Both connect to the
// gateway (/_tjena/move/v1/send and /receive), which only pipes the data
// through. The old phone stops its bridge and sends the bridge database and
// secrets encrypted with the key (Backup.exportForMove), so the server never
// sees them. Once the new phone reports "imported", the old phone removes its
// copy. The new phone then takes over the bridge slot (fresh tokens, same
// bridge bot), so tjena keeps all chats and WhatsApp stays linked.
//
// Without a camera the new phone connects without the code ("Connect without
// a code"); the phones agree on a key with ECDH through the gateway and both
// show a 6-digit number the user compares (MoveKex.kt, docker/manager/kex.go).
//
// Messages: both get {"type":"paired"}; new → old {"type":"hello","mode":
// "code","proof":…} or {"type":"hello","mode":"compare","commit":…} (then
// "key" both ways and "confirm" from the new phone); old → new
// {"type":"meta","size":N}, binary chunks, {"type":"end"}; new → old
// {"type":"received"}, then {"type":"imported","proof":…} or
// {"type":"error","message":…}. The old phone removes its copy only after an
// "imported" with a valid proof.

/// Content of the old phone's QR code:
/// `tjena-bridge://move?id=…&key=…&gw=https://wap.tjena.eu`
class MoveCode {
  final String id;
  final String key; // base64url, 32 bytes
  final String gateway;
  MoveCode(this.id, this.key, this.gateway);

  /// No code: the new phone reaches its own bridge's move and compares numbers.
  MoveCode.withoutCode(this.gateway)
      : id = '',
        key = '';
  bool get withoutCode => id.isEmpty;

  static MoveCode create(String gateway) {
    final r = Random.secure();
    String rand(int n) => base64Url.encode(List.generate(n, (_) => r.nextInt(256))).replaceAll('=', '');
    return MoveCode(rand(18), rand(32), gateway);
  }

  String get uri => Uri(
        scheme: 'tjena-bridge',
        host: 'move',
        queryParameters: {'id': id, 'key': key, 'gw': gateway},
      ).toString();

  static MoveCode? parse(String raw) {
    final u = Uri.tryParse(raw.trim());
    if (u == null || u.scheme != 'tjena-bridge' || u.host != 'move') return null;
    final q = u.queryParameters;
    final id = q['id'], key = q['key'], gw = q['gw'] ?? defaultGateway;
    final g = Uri.tryParse(gw);
    if (id == null || key == null || g == null || (g.scheme != 'https' && g.scheme != 'http')) return null;
    return MoveCode(id, key, gw);
  }

  Uri wsUri(String role) {
    final g = Uri.parse(gateway.endsWith('/') ? gateway.substring(0, gateway.length - 1) : gateway);
    return g.replace(
      scheme: g.scheme == 'https' ? 'wss' : 'ws',
      path: '${g.path}/_tjena/move/v1/$role',
      queryParameters: id.isEmpty ? null : {'id': id},
    );
  }
}

Map<String, dynamic>? _json(dynamic m) {
  if (m is! String) return null;
  try {
    final v = jsonDecode(m);
    return v is Map<String, dynamic> ? v : null;
  } catch (_) {
    return null;
  }
}

String _mb(int bytes) => '${(bytes / (1 << 20)).toStringAsFixed(1)} MB';

// ---------------------------------------------------------------------------
// Old phone

enum _OutPhase { waiting, compare, sending, importing, done, unsure, failed }

/// Settings → "Move to a new phone" (old phone).
class MoveOutScreen extends StatefulWidget {
  final BridgeState state;
  final Future<void> Function() refresh;
  const MoveOutScreen({required this.state, required this.refresh, super.key});
  @override
  State<MoveOutScreen> createState() => _MoveOutScreenState();
}

class _MoveOutScreenState extends State<MoveOutScreen> {
  late MoveCode _code;
  WebSocket? _ws;
  _OutPhase _phase = _OutPhase.waiting;
  String? _error;
  int _sent = 0, _size = 0;
  bool _stopped = false; // our bridge is stopped for the move
  bool _received = false; // the new phone has everything
  // Moving without a code: the agreed key, the number, who confirmed.
  String? _key, _commit, _number;
  bool _kex = false, _localOk = false, _remoteOk = false, _sending = false;

  @override
  void initState() {
    super.initState();
    _start();
  }

  @override
  void dispose() {
    _ws?.close();
    // Left the screen before the new phone had everything: keep the bridge here.
    if (_stopped && !_received) Native.start();
    Native.moveCleanup();
    super.dispose();
  }

  Future<void> _start() async {
    _code = MoveCode.create(widget.state.gatewayUrl);
    setState(() {
      _phase = _OutPhase.waiting;
      _error = null;
      _sent = _size = 0;
      _received = false;
      _key = _commit = _number = null;
      _kex = _localOk = _remoteOk = _sending = false;
    });
    try {
      final token = await Native.moveToken();
      final ws = _ws = await WebSocket.connect(
        _code.wsUri('send').toString(),
        headers: {'Authorization': 'Bearer $token'},
      ).timeout(const Duration(seconds: 20));
      ws.pingInterval = const Duration(seconds: 20);
      final done = Completer<void>();
      ws.listen(
        (m) => _onMessage(ws, m),
        onDone: () => done.complete(),
        onError: (_) => done.complete(),
        cancelOnError: true,
      );
      await done.future;
      _closed();
    } catch (e) {
      _fail('Can\'t reach the tjena server: $e');
    }
  }

  Future<void> _onMessage(WebSocket ws, dynamic m) async {
    final msg = _json(m);
    try {
      switch (msg?['type']) {
        case 'hello':
          if (_sending || _kex) return;
          if (msg?['mode'] == 'code') {
            if (!await Native.moveCheckProof(_code.key, Native.proofHello, msg?['proof'] as String?)) {
              _fail('The new phone has a different code. Try again.');
              await ws.close();
              return;
            }
            await _send(ws, _code.key);
          } else if (msg?['mode'] == 'compare') {
            _kex = true;
            _commit = msg?['commit'] as String?;
            final k = await Native.moveKexStart();
            ws.add(jsonEncode({'type': 'key', 'pub': k.pub}));
          }
        case 'key':
          if (!_kex || _key != null) return;
          final r = await Native.moveKexDerive(peer: msg?['pub'] as String? ?? '', oldSide: true, commit: _commit ?? '');
          _key = r.key;
          if (mounted) {
            setState(() {
              _number = r.code;
              _phase = _OutPhase.compare;
            });
          }
        case 'confirm':
          _remoteOk = true;
          await _maybeSend(ws);
        case 'received':
          _received = true;
          if (mounted) setState(() => _phase = _OutPhase.importing);
        case 'imported':
          if (_key == null || !await Native.moveCheckProof(_key!, Native.proofImported, msg?['proof'] as String?)) {
            return; // not from the new phone: ignore
          }
          if (mounted) setState(() => _phase = _OutPhase.done);
          await _ws?.close();
          // The new phone runs the bridge now: remove it here (local only; the
          // bridge slot stays, the new phone uses it).
          await Native.wipe();
        case 'error':
          _fail('The new phone could not take over: ${msg?['message'] ?? 'unknown error'}');
          await _ws?.close();
      }
    } on PlatformException catch (e) {
      _fail(e.message ?? e.code);
      await ws.close();
    }
  }

  Future<void> _answer(bool match) async {
    final ws = _ws;
    if (ws == null || _key == null) return;
    if (!match) {
      _fail('Cancelled: the numbers didn\'t match. Nothing was sent.');
      await ws.close();
      return;
    }
    setState(() => _localOk = true);
    await _maybeSend(ws);
  }

  Future<void> _maybeSend(WebSocket ws) async {
    if (_localOk && _remoteOk && _key != null && !_sending) await _send(ws, _key!);
  }

  Future<void> _send(WebSocket ws, String key) async {
    _key = key;
    _sending = true;
    setState(() => _phase = _OutPhase.sending);
    try {
      _stopped = true;
      final path = await Native.moveExport(key);
      final f = File(path);
      _size = await f.length();
      ws.add(jsonEncode({'type': 'meta', 'size': _size}));
      await ws.addStream(f.openRead().map((chunk) {
        _sent += chunk.length;
        if (mounted) setState(() {});
        return chunk;
      }));
      ws.add(jsonEncode({'type': 'end'}));
      await f.delete();
    } catch (e) {
      _fail('Sending failed: $e');
      await ws.close();
    }
  }

  void _closed() {
    if (!mounted || _phase == _OutPhase.done || _phase == _OutPhase.failed) return;
    if (_received) {
      // The new phone has everything but we don't know whether it finished.
      // Starting the bridge here again could run WhatsApp twice: ask.
      setState(() => _phase = _OutPhase.unsure);
    } else {
      _fail(_phase == _OutPhase.waiting || _phase == _OutPhase.compare
          ? 'The connection to the tjena server ended.'
          : 'The transfer was interrupted.');
    }
  }

  void _fail(String text) {
    if (!mounted) return;
    setState(() {
      _phase = _OutPhase.failed;
      _error = text;
    });
    if (_stopped && !_received) {
      // Nothing usable reached the new phone: keep the bridge here.
      _stopped = false;
      Native.start();
    }
  }

  Future<void> _keepHere() async {
    _stopped = false;
    await Native.start();
    if (mounted) Navigator.of(context).pop();
  }

  Future<void> _removeHere() async {
    await Native.wipe();
    await _close();
  }

  /// The bridge is gone from this phone: back to the setup screen.
  Future<void> _close() async {
    if (mounted) Navigator.of(context).popUntil((r) => r.isFirst);
    await widget.refresh();
  }

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context);
    return Scaffold(
      appBar: AppBar(title: const Text('Move to a new phone')),
      body: ListView(
        padding: const EdgeInsets.all(20),
        children: switch (_phase) {
          _OutPhase.waiting => [
              const Text(
                'On the new phone: install tjena Bridge, sign in with the same tjena account and choose '
                '"Move from my old phone". Then scan this code.',
              ),
              const SizedBox(height: 20),
              Center(
                child: Container(
                  color: Colors.white,
                  padding: const EdgeInsets.all(16),
                  child: SizedBox(
                    width: 260,
                    height: 260,
                    child: PrettyQrView.data(data: _code.uri),
                  ),
                ),
              ),
              Center(
                child: TextButton.icon(
                  onPressed: () async {
                    await Clipboard.setData(ClipboardData(text: _code.uri));
                    if (context.mounted) toast(context, 'Code copied');
                  },
                  icon: const Icon(Icons.copy),
                  label: const Text('Copy code'),
                ),
              ),
              const SizedBox(height: 12),
              Text(
                'Your chats, the WhatsApp link and the encryption keys move over, encrypted with a key that '
                'is only in this code. The tjena server only passes the data on. Don\'t show this code to anyone '
                'else.',
                style: t.textTheme.bodySmall,
              ),
              const SizedBox(height: 12),
              const Text(
                'New phone without a camera, or a Docker bridge? Choose "Connect without a code" there. '
                'Both then show a number to compare.',
              ),
              const SizedBox(height: 8),
              const Text('The bridge keeps running here until the new phone connects.'),
            ],
          _OutPhase.compare => [
              NumberCheck(number: _number ?? '', confirmed: _localOk, onAnswer: _answer, otherDevice: 'the new phone'),
            ],
          _OutPhase.sending || _OutPhase.importing => [
              Text(
                _phase == _OutPhase.sending ? 'Sending to the new phone…' : 'The new phone is setting up the bridge…',
                style: t.textTheme.titleMedium,
              ),
              const SizedBox(height: 12),
              LinearProgressIndicator(value: _phase == _OutPhase.sending && _size > 0 ? _sent / _size : null),
              const SizedBox(height: 8),
              if (_size > 0) Text('${_mb(_sent)} of ${_mb(_size)}'),
              const SizedBox(height: 12),
              const Text('The bridge is paused here. Keep both phones on and this screen open.'),
            ],
          _OutPhase.done => [
              Text('Moved.', style: t.textTheme.titleMedium),
              const SizedBox(height: 8),
              const Text(
                'The new phone runs your bridge now, and the bridge data was removed from this phone. '
                'You can uninstall tjena Bridge here.',
              ),
              const SizedBox(height: 16),
              FilledButton(onPressed: _close, child: const Text('OK')),
            ],
          _OutPhase.unsure => [
              const WarningCard(
                title: 'Did the new phone finish?',
                text: 'The new phone received everything, but the connection ended before it confirmed. '
                    'Look at the new phone. Only one phone may run the bridge.',
              ),
              FilledButton(
                onPressed: _removeHere,
                child: const Text('It works on the new phone: remove it here'),
              ),
              const SizedBox(height: 8),
              OutlinedButton(onPressed: _keepHere, child: const Text('It failed: keep the bridge on this phone')),
            ],
          _OutPhase.failed => [
              ErrorText(_error ?? 'The move failed.'),
              const SizedBox(height: 8),
              const Text('The bridge keeps running on this phone.'),
              const SizedBox(height: 16),
              FilledButton(onPressed: _start, child: const Text('Try again')),
            ],
        },
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// New phone

/// Receives the bridge from the old phone and makes this phone the active
/// bridge. [session] is this phone's new tjena session. Reports progress
/// through [progress]. With [MoveCode.withoutCode] the phones compare a
/// number: [confirmNumber] shows it and completes with the user's answer.
Future<void> moveIn({
  required MoveCode code,
  required MatrixSession session,
  required void Function(String text, double? fraction) progress,
  required Future<bool> Function(String number) confirmNumber,
}) async {
  progress('Connecting to your old phone…', null);
  final WebSocket ws;
  try {
    ws = await WebSocket.connect(
      code.wsUri('receive').toString(),
      headers: {'Authorization': 'Bearer ${session.accessToken}'},
    ).timeout(const Duration(seconds: 20));
  } on WebSocketException catch (e) {
    throw Exception(!e.message.contains('404')
        ? 'Can\'t reach the tjena server: ${e.message}'
        : code.withoutCode
            ? 'Your old phone isn\'t ready yet. On it open tjena Bridge → Settings → Move to a new phone, then try again.'
            : 'Your old phone isn\'t waiting for this code. Open "Move to a new phone" on it again.');
  }
  ws.pingInterval = const Duration(seconds: 20);
  final path = await Native.moveInPath();
  final file = File(path);
  final sink = file.openWrite();
  var size = 0, got = 0;
  String? key = code.withoutCode ? null : code.key;
  String? myPub; // sent after the old phone's key (we committed to it first)
  var confirmed = !code.withoutCode, finished = false, kex = false;
  final ended = Completer<void>();
  void stop(String why) {
    if (!ended.isCompleted) ended.completeError(Exception(why));
  }

  Future<void> onText(Map<String, dynamic>? msg) async {
    switch (msg?['type']) {
      case 'paired':
        if (code.withoutCode) {
          final k = await Native.moveKexStart();
          kex = true;
          ws.add(jsonEncode({'type': 'hello', 'mode': 'compare', 'commit': k.commit}));
          myPub = k.pub;
          progress('Connected. Exchanging keys…', null);
        } else {
          ws.add(jsonEncode({'type': 'hello', 'mode': 'code', 'proof': await Native.moveProof(code.key, Native.proofHello)}));
          progress('Your old phone is preparing the bridge…', null);
        }
      case 'key':
        if (!kex || key != null) return;
        final r = await Native.moveKexDerive(peer: msg?['pub'] as String? ?? '', oldSide: false);
        ws.add(jsonEncode({'type': 'key', 'pub': myPub}));
        key = r.key;
        progress('Check that both phones show the same number.', null);
        final ok = await confirmNumber(r.code);
        if (finished || ended.isCompleted) return;
        if (!ok) {
          stop('Cancelled: the numbers didn\'t match. Nothing was received.');
          return;
        }
        confirmed = true;
        ws.add(jsonEncode({'type': 'confirm'}));
        progress('Confirm on your old phone too…', null);
      case 'meta':
        if (!confirmed) return stop('The old phone sent data before the numbers were checked.');
        size = (msg?['size'] as num?)?.toInt() ?? 0;
      case 'end':
        if (!ended.isCompleted) ended.complete();
    }
  }

  final sub = ws.listen(
    (m) {
      if (m is List<int>) {
        if (!confirmed) return stop('The old phone sent data before the numbers were checked.');
        sink.add(m);
        got += m.length;
        progress('Receiving from your old phone…', size > 0 ? got / size : null);
        return;
      }
      onText(_json(m)).catchError((Object e) => stop(e is PlatformException ? (e.message ?? e.code) : '$e'));
    },
    onDone: () => stop('Your old phone stopped the transfer.'),
    onError: (Object e) => stop('The transfer was interrupted: $e'),
  );
  try {
    await ended.future.timeout(const Duration(minutes: 30));
    await sink.close();
    ws.add(jsonEncode({'type': 'received'}));
    progress('Setting up the bridge on this phone…', null);
    try {
      await Native.moveImport(path, key!);
    } catch (e) {
      ws.add(jsonEncode({'type': 'error', 'message': e.toString()}));
      rethrow;
    }
    ws.add(jsonEncode({'type': 'imported', 'proof': await Native.moveProof(key!, Native.proofImported)}));
  } finally {
    finished = true;
    await sub.cancel();
    await ws.close();
    try {
      await sink.close();
    } catch (_) {}
    try {
      if (await file.exists()) await file.delete();
    } catch (_) {}
  }

  // Take over the slot: fresh bridge tokens (the old phone's stop working),
  // same bridge bot, so all chats in tjena stay as they are.
  progress('Making this phone the active bridge…', null);
  final old = await Native.session();
  try {
    final bundle = await Provisioner(code.gateway, session.accessToken).claim();
    await Native.saveSetup(bundle: bundle, session: session.toJson(), gateway: code.gateway);
    if (old != null) {
      final o = MatrixSession.fromJson(old);
      if (o.deviceId != session.deviceId) await logout(o); // the old phone's tjena session
    }
  } catch (_) {
    // The moved setup still works as it is (the old phone has removed its
    // copy); "Make this phone the active bridge" in Settings can retry.
    await logout(session);
  }
  await Native.start();
}

/// The number both devices show when moving without a code.
class NumberCheck extends StatelessWidget {
  final String number;
  final bool confirmed;
  final void Function(bool match) onAnswer;
  final String otherDevice;
  const NumberCheck({
    required this.number,
    required this.confirmed,
    required this.onAnswer,
    required this.otherDevice,
    super.key,
  });

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context);
    final shown = number.length == 6 ? '${number.substring(0, 3)} ${number.substring(3)}' : number;
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Text('Does $otherDevice show the same number?', style: t.textTheme.titleMedium),
      const SizedBox(height: 16),
      Container(
        padding: const EdgeInsets.symmetric(vertical: 16),
        decoration: BoxDecoration(color: t.colorScheme.surfaceContainerHighest, borderRadius: BorderRadius.circular(12)),
        child: Text(
          shown,
          textAlign: TextAlign.center,
          semanticsLabel: number.split('').join(' '),
          style: t.textTheme.displayMedium?.copyWith(fontFeatures: const [FontFeature.tabularFigures()], letterSpacing: 4),
        ),
      ),
      const SizedBox(height: 16),
      if (confirmed) ...[
        const LinearProgressIndicator(),
        const SizedBox(height: 8),
        Text('Confirmed here. Now confirm on $otherDevice too.'),
      ] else ...[
        FilledButton(onPressed: () => onAnswer(true), child: const Text('Same number')),
        const SizedBox(height: 8),
        OutlinedButton(onPressed: () => onAnswer(false), child: const Text('Different number')),
        const SizedBox(height: 12),
        Text(
          'If the numbers differ, someone may be trying to intercept the move. Nothing has been sent yet.',
          style: t.textTheme.bodySmall,
        ),
      ],
    ]);
  }
}
