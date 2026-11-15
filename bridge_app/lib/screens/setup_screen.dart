import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:qr_code_scanner_plus/qr_code_scanner_plus.dart';

import '../matrix_api.dart';
import '../native.dart';
import 'backup_screen.dart';
import 'move_screen.dart';
import 'widgets.dart';

/// Screen 1: sign in with the tjena account, get a bridge slot, start.
class SetupScreen extends StatefulWidget {
  final Future<void> Function() onDone;
  const SetupScreen({required this.onDone, super.key});
  @override
  State<SetupScreen> createState() => _SetupScreenState();
}

class _SetupScreenState extends State<SetupScreen> {
  bool _acceptedRisk = false;
  bool _busy = false;
  String? _progress;
  String? _error;
  bool _showManual = false;
  bool _showAdvanced = false;
  bool _camera = true;
  // Moving without a code: the number to compare and the user's answer.
  String? _number;
  Completer<bool>? _numberAnswer;
  bool _numberConfirmed = false;

  @override
  void initState() {
    super.initState();
    Native.hasCamera().then((c) {
      if (mounted) {
        setState(() {
          _camera = c;
          if (!c) _showManual = true; // no QR sign-in without a camera
        });
      }
    });
  }

  Future<bool> _confirmNumber(String number) {
    final c = Completer<bool>();
    setState(() {
      _number = number;
      _numberAnswer = c;
      _numberConfirmed = false;
    });
    return c.future;
  }

  void _answerNumber(bool match) {
    final c = _numberAnswer;
    if (c == null || c.isCompleted) return;
    c.complete(match);
    setState(() {
      if (match) {
        _numberConfirmed = true;
      } else {
        _number = null;
      }
    });
  }
  final _hs = TextEditingController(text: defaultHomeserver);
  final _gw = TextEditingController(text: defaultGateway);
  final _user = TextEditingController();
  final _pass = TextEditingController();

  Future<void> _scan() async {
    final raw = await Navigator.of(context).push<String>(MaterialPageRoute(builder: (_) => const _QrScanPage()));
    if (raw == null) return;
    final link = PairingLink.parse(raw);
    if (link == null) {
      setState(() => _error = 'This is not a tjena bridge QR code. Open tjena → Settings → Bridges → WhatsApp → My own bridge.');
      return;
    }
    _hs.text = link.homeserver;
    _gw.text = link.gateway;
    if (link.user != null) _user.text = link.user!;
    if (link.loginToken == null) {
      setState(() {
        _showManual = true;
        _error = 'Your server does not support QR sign-in. Enter your tjena password below instead.';
      });
      return;
    }
    await _run(() => login(homeserver: link.homeserver, loginToken: link.loginToken));
  }

  Future<void> _manual() async {
    final user = _user.text.trim();
    if (user.isEmpty || _pass.text.isEmpty) {
      setState(() => _error = 'Enter your tjena username and password.');
      return;
    }
    await _run(() => login(homeserver: _hs.text.trim(), user: user, password: _pass.text));
  }

  Future<void> _run(Future<MatrixSession> Function() doLogin) async {
    setState(() {
      _busy = true;
      _error = null;
      _progress = 'Signing in…';
    });
    MatrixSession? session;
    try {
      session = await doLogin();
      setState(() => _progress = 'Signed in as ${session!.userId}. Requesting your bridge…');
      final prov = Provisioner(_gw.text.trim(), session.accessToken);
      // Already a bridge for this account (another phone)? Move it here
      // instead of starting over.
      final existing = await prov.status();
      if (existing != null && existing['state'] == 'assigned' && mounted) {
        final choice = await _askExisting(existing);
        if (!mounted) return;
        switch (choice) {
          case _Existing.move:
            final code = await Navigator.of(context).push<MoveCode>(
                MaterialPageRoute(builder: (_) => _MoveInChoicePage(gateway: _gw.text.trim(), camera: _camera)));
            if (code == null) {
              await logout(session);
              return;
            }
            await moveIn(
              code: code,
              session: session,
              progress: (text, _) {
                if (mounted) setState(() => _progress = text);
              },
              confirmNumber: _confirmNumber,
            );
            _pass.clear();
            await widget.onDone();
            return;
          case _Existing.restore:
            await logout(session); // the backup brings its own session
            if (!mounted) return;
            await Navigator.of(context).push(MaterialPageRoute(builder: (_) => const RestoreScreen()));
            await widget.onDone();
            return;
          case _Existing.fresh:
            break; // below: same bridge slot with new tokens and an empty bridge
          case null:
            await logout(session);
            return;
        }
      }
      var bundle = await prov.claim();
      // A slot is usable once Synapse has loaded it. With the slot pool this is
      // normally immediate; otherwise wait for the admin's planned restart.
      while (bundle['active'] != true) {
        setState(() => _progress = 'Waiting for server activation…\nThis bridge slot becomes usable after the server\'s next planned restart. You can leave this screen open.');
        await Future.delayed(const Duration(seconds: 15));
        final st = await prov.status();
        if (st != null && st['active'] == true) bundle['active'] = true;
        if (!mounted) return;
      }
      setState(() => _progress = 'Configuring the bridge…');
      await Native.saveSetup(bundle: bundle, session: session.toJson(), gateway: _gw.text.trim());
      await Native.start();
      _pass.clear();
      await widget.onDone();
    } on ApiException catch (e) {
      if (session != null && e.status != 0) await logout(session);
      setState(() => _error = e.status == 503
          ? 'The server has no free bridge slots right now. Ask the tjena admin to add more.'
          : e.status == 429
              ? 'Too many setup attempts. Please try again in an hour.'
              : 'Setup failed: ${e.message}');
    } catch (e) {
      // Not set up after all (e.g. the move failed): drop the unused session.
      if (session != null && !(await Native.state()).setUp) await logout(session);
      if (mounted) setState(() => _error = 'Setup failed: ${e.toString().replaceFirst('Exception: ', '')}');
    } finally {
      if (_numberAnswer?.isCompleted == false) _numberAnswer!.complete(false);
      _numberAnswer = null;
      _number = null;
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<_Existing?> _askExisting(Map<String, dynamic> st) {
    final online = st['connected'] == true;
    final seen = DateTime.tryParse(st['last_seen'] as String? ?? '')?.toLocal();
    final where = online
        ? 'Your other bridge phone is online right now.'
        : seen != null
            ? 'Your other bridge phone was last online ${seen.toString().substring(0, 16)}.'
            : 'Your other bridge phone is offline.';
    return showDialog<_Existing>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('You already have a bridge'),
        content: SingleChildScrollView(
          child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(where),
            const SizedBox(height: 12),
            const Text('Move it to this phone to keep your chats and the WhatsApp link. '
                'This works with or without a camera.'),
          ]),
        ),
        actionsOverflowDirection: VerticalDirection.down,
        actionsOverflowButtonSpacing: 4,
        actions: [
          FilledButton.icon(
            onPressed: () => Navigator.pop(ctx, _Existing.move),
            icon: const Icon(Icons.phonelink_ring),
            label: const Text('Move from my old phone'),
          ),
          OutlinedButton.icon(
            onPressed: () => Navigator.pop(ctx, _Existing.restore),
            icon: const Icon(Icons.restore),
            label: const Text('Restore from a backup file'),
          ),
          TextButton(
            onPressed: () async {
              final ok = await confirm(
                ctx,
                'Start fresh?',
                'Use this if the old phone is lost or broken. This phone takes over your bridge with an empty '
                    'setup: you link WhatsApp again, and the old phone stops. Chats already in tjena stay, but '
                    'WhatsApp chats may appear a second time, as new chats.',
                ok: 'Start fresh',
                danger: true,
              );
              if (ok && ctx.mounted) Navigator.pop(ctx, _Existing.fresh);
            },
            child: const Text('Start fresh (old phone lost)'),
          ),
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Cancel')),
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context);
    return Scaffold(
      appBar: AppBar(title: const Text('Set up your WhatsApp bridge')),
      body: ListView(
        padding: const EdgeInsets.all(20),
        children: [
          Text('This phone will run your personal WhatsApp bridge for tjena.', style: t.textTheme.titleMedium),
          const SizedBox(height: 8),
          const Text(
            'Keep it at home, on Wi-Fi and on a charger. Your WhatsApp messages are only decrypted on this phone '
            'and on your own tjena devices — the tjena server only relays encrypted data.',
          ),
          const SizedBox(height: 16),
          const WarningCard(
            title: 'Unofficial WhatsApp client',
            text: 'The bridge connects to WhatsApp as a "linked device" using an unofficial client. '
                'WhatsApp may restrict or ban accounts that use unofficial clients. Use at your own risk.',
          ),
          CheckboxListTile(
            value: _acceptedRisk,
            onChanged: _busy ? null : (v) => setState(() => _acceptedRisk = v ?? false),
            title: const Text('I understand the risk'),
            controlAffinity: ListTileControlAffinity.leading,
            contentPadding: EdgeInsets.zero,
          ),
          const SizedBox(height: 8),
          if (_busy && _number != null) ...[
            NumberCheck(
              number: _number!,
              confirmed: _numberConfirmed,
              onAnswer: _answerNumber,
              otherDevice: 'your old phone',
            ),
          ] else if (_busy) ...[
            const LinearProgressIndicator(),
            const SizedBox(height: 12),
            Text(_progress ?? ''),
          ] else ...[
            Text('Sign in with your tjena account', style: t.textTheme.titleSmall),
            const SizedBox(height: 4),
            Text(
              'Same account as in the tjena app. If you already have a bridge on another device, you can move it '
              'here after signing in.',
              style: t.textTheme.bodySmall,
            ),
            const SizedBox(height: 12),
            if (_camera) ...[
              FilledButton.icon(
                onPressed: _acceptedRisk ? _scan : null,
                icon: const Icon(Icons.qr_code_scanner),
                label: const Text('Scan the QR code from tjena'),
              ),
              const SizedBox(height: 4),
              Text(
                'In the tjena app: Settings → Bridges → WhatsApp → My own bridge → Set up. '
                'Your password is never typed on this phone.',
                style: t.textTheme.bodySmall,
              ),
              const SizedBox(height: 8),
              FilledButton.tonalIcon(
                onPressed: () => setState(() => _showManual = !_showManual),
                icon: const Icon(Icons.password),
                label: const Text('Sign in with username and password'),
              ),
            ],
            if (_showManual) ...[
              TextField(controller: _user, decoration: const InputDecoration(labelText: 'tjena username or @user:tjena.eu')),
              TextField(controller: _pass, obscureText: true, decoration: const InputDecoration(labelText: 'Password')),
              const SizedBox(height: 8),
              FilledButton(onPressed: _acceptedRisk ? _manual : null, child: const Text('Sign in')),
            ],
            const SizedBox(height: 8),
            OutlinedButton.icon(
              onPressed: () async {
                await Navigator.of(context).push(MaterialPageRoute(builder: (_) => const RestoreScreen()));
                await widget.onDone();
              },
              icon: const Icon(Icons.restore),
              label: const Text('Restore from a backup file'),
            ),
            TextButton(
              onPressed: () => setState(() => _showAdvanced = !_showAdvanced),
              child: const Text('Advanced'),
            ),
            if (_showAdvanced) ...[
              TextField(controller: _hs, decoration: const InputDecoration(labelText: 'Homeserver (for sign-in)')),
              TextField(controller: _gw, decoration: const InputDecoration(labelText: 'Bridge proxy (gateway)')),
            ],
          ],
          if (_error != null) ...[
            const SizedBox(height: 16),
            ErrorText(_error!),
          ],
        ],
      ),
    );
  }
}

enum _Existing { move, restore, fresh }

/// How to reach the old phone: scan its code, connect without a code (compare
/// a number), or paste the code. Returns the code, or null when cancelled.
class _MoveInChoicePage extends StatefulWidget {
  final String gateway;
  final bool camera;
  const _MoveInChoicePage({required this.gateway, required this.camera});
  @override
  State<_MoveInChoicePage> createState() => _MoveInChoicePageState();
}

class _MoveInChoicePageState extends State<_MoveInChoicePage> {
  final _paste = TextEditingController();
  bool _showPaste = false;
  String? _error;

  Future<void> _scan() async {
    final raw = await Navigator.of(context)
        .push<String>(MaterialPageRoute(builder: (_) => const _QrScanPage(title: 'Scan the code on your old phone')));
    if (raw == null || !mounted) return;
    _use(raw);
  }

  void _use(String raw) {
    final code = MoveCode.parse(raw);
    if (code == null) {
      setState(() => _error = 'This is not a move code. On the old phone open tjena Bridge → Settings → Move to a new phone.');
      return;
    }
    Navigator.of(context).pop(code);
  }

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context);
    return Scaffold(
      appBar: AppBar(title: const Text('Move from my old phone')),
      body: ListView(
        padding: const EdgeInsets.all(20),
        children: [
          Text('1. On the old phone', style: t.textTheme.titleSmall),
          const SizedBox(height: 4),
          const Text('Open tjena Bridge → Settings → Move to a new phone. '
              'From a Docker bridge: Backup & move → Move to another device.'),
          const SizedBox(height: 16),
          Text('2. On this phone', style: t.textTheme.titleSmall),
          const SizedBox(height: 8),
          if (widget.camera) ...[
            FilledButton.icon(
              onPressed: _scan,
              icon: const Icon(Icons.qr_code_scanner),
              label: const Text('Scan the code on the old phone'),
            ),
            const SizedBox(height: 8),
          ],
          FilledButton.icon(
            style: widget.camera
                ? FilledButton.styleFrom(
                    backgroundColor: t.colorScheme.secondaryContainer,
                    foregroundColor: t.colorScheme.onSecondaryContainer,
                  )
                : null,
            onPressed: () => Navigator.of(context).pop(MoveCode.withoutCode(widget.gateway)),
            icon: const Icon(Icons.pin_outlined),
            label: const Text('Connect without a code'),
          ),
          const SizedBox(height: 4),
          Text(
            'No camera needed: both phones then show a 6-digit number. If it\'s the same, confirm on both.',
            style: t.textTheme.bodySmall,
          ),
          const SizedBox(height: 8),
          TextButton(
            onPressed: () => setState(() => _showPaste = !_showPaste),
            child: const Text('Paste the code instead'),
          ),
          if (_showPaste) ...[
            TextField(
              controller: _paste,
              minLines: 2,
              maxLines: 4,
              decoration: const InputDecoration(labelText: 'tjena-bridge://move?…'),
            ),
            const SizedBox(height: 8),
            FilledButton.tonal(onPressed: () => _use(_paste.text), child: const Text('Use this code')),
          ],
          if (_error != null) ...[
            const SizedBox(height: 16),
            ErrorText(_error!),
          ],
        ],
      ),
    );
  }
}

class _QrScanPage extends StatefulWidget {
  final String title;
  const _QrScanPage({this.title = 'Scan the QR code from tjena'});
  @override
  State<_QrScanPage> createState() => _QrScanPageState();
}

class _QrScanPageState extends State<_QrScanPage> {
  final _key = GlobalKey(debugLabel: 'QR');
  QRViewController? _controller;
  StreamSubscription? _sub;

  @override
  void reassemble() {
    super.reassemble();
    if (Platform.isAndroid) _controller?.pauseCamera();
  }

  @override
  void dispose() {
    _sub?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: Text(widget.title)),
      body: QRView(
        key: _key,
        onQRViewCreated: (c) {
          _controller = c;
          final nav = Navigator.of(context);
          // Scanner can start paused on Android.
          c.pauseCamera();
          c.resumeCamera();
          _sub = c.scannedDataStream.listen((d) {
            final code = d.code;
            if (code == null || !code.startsWith('tjena-bridge://')) return;
            _sub?.cancel();
            if (mounted) nav.pop(code);
          });
        },
        overlay: QrScannerOverlayShape(
          borderColor: Theme.of(context).colorScheme.primary,
          borderRadius: 10,
          borderLength: 30,
          borderWidth: 8,
        ),
      ),
    );
  }
}
