import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../native.dart';
import 'widgets.dart';

/// Progress and diagnostics of the WhatsApp history import: what the phone
/// sent, which chats got a room, how many messages were copied, and which
/// chats stopped early (and why). Reads the bridge's database and log; it
/// changes nothing.
class HistoryScreen extends StatefulWidget {
  const HistoryScreen({super.key});
  @override
  State<HistoryScreen> createState() => _HistoryScreenState();
}

class _HistoryScreenState extends State<HistoryScreen> {
  Map<String, dynamic>? _info;
  String? _error;
  Timer? _timer;
  bool _onlyProblems = false;
  bool _loading = false;
  bool _catchingUp = false;
  bool _stopping = false;
  int _tab = 0;
  bool _selecting = false;
  final Set<String> _selected = {};
  Map<String, dynamic> _pace = {};
  String _appVersion = '';
  Map<String, dynamic> _dp = {};

  @override
  void initState() {
    super.initState();
    _load();
    _timer = Timer.periodic(const Duration(seconds: 5), (_) {
      if (_running) _load();
    });
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _load() async {
    if (_loading) return;
    _loading = true;
    try {
      final info = await Native.historyInfo();
      Map<String, dynamic> pace = {};
      var appVersion = '';
      try {
        final st = await Native.state();
        pace = st.pace;
        appVersion = st.appVersion;
      } catch (_) {}
      Map<String, dynamic> dp = {};
      try {
        dp = await Native.doublePuppet('status');
      } catch (_) {}
      if (mounted) {
        setState(() {
          _dp = dp;
          _info = info;
          _pace = pace;
          _appVersion = appVersion;
          _error = null;
        });
      }
    } catch (e) {
      if (mounted) {
        setState(
          () => _error = e is PlatformException ? (e.message ?? '$e') : '$e',
        );
      }
    } finally {
      _loading = false;
    }
  }

  Map<String, dynamic> get _log =>
      (_info?['log'] as Map?)?.cast<String, dynamic>() ?? {};
  List<Map<String, dynamic>> get _logins => ((_info?['logins'] as List?) ?? [])
      .cast<Map>()
      .map((m) => m.cast<String, dynamic>())
      .toList();

  int _sum(String k) =>
      _logins.fold(0, (a, l) => a + ((l[k] as num?)?.toInt() ?? 0));

  /// Why the screen thinks the import still runs (null = finished).
  String? get _runningWhy {
    if (_info == null) return null;
    final waiting = (_pace['sends_waiting'] as num?)?.toInt() ?? 0;
    if (waiting > 0) return '$waiting messages are still being sent';
    final pending = _sum('pending_chunks');
    if (pending > 0) {
      return '$pending history pieces from your phone not read yet';
    }
    if (_logins.any((l) => l['rooms_pending'] == true)) {
      return 'the bridge has not finished creating chats from history';
    }
    final log = _log;
    if (log['rooms_creating'] == true && log['rooms_done'] != true) {
      return 'chats are still being created';
    }
    return null;
  }

  bool get _running {
    if (_info == null) return true;
    if (((_pace['sends_waiting'] as num?) ?? 0) > 0) return true;
    final log = _log;
    if (_sum('pending_chunks') > 0) return true;
    if (_logins.any((l) => l['rooms_pending'] == true)) return true;
    return log['rooms_creating'] == true && log['rooms_done'] != true;
  }

  Future<void> _resumeDP() async {
    final messenger = ScaffoldMessenger.of(context);
    try {
      await Native.doublePuppet('resume');
      messenger.showSnackBar(
        const SnackBar(
          content: Text('Double puppeting is on again. The bridge restarted.'),
        ),
      );
    } catch (e) {
      messenger.showSnackBar(SnackBar(content: Text('Failed: $e')));
    }
    _load();
  }

  Map<String, dynamic>? get _login =>
      _logins.isEmpty ? null : _logins[_tab.clamp(0, _logins.length - 1)];

  List<Map<String, dynamic>> _chatsOf(Map<String, dynamic>? login) =>
      ((login?['chats'] as List?) ?? [])
          .cast<Map>()
          .map((c) => c.cast<String, dynamic>())
          .toList();

  /// The relay's verdict for a chat: "ok", "fixable", "unfixable", "held"
  /// or "" (see relay history.go, HistoryChat.State).
  static String _state(Map<String, dynamic> c) {
    final s = c['state'] as String?;
    if (s != null) return s;
    // Older relay without states.
    final stored = ((c['stored'] as num?) ?? 0).toInt();
    final fill = ((c['fillable'] as num?) ?? 0).toInt();
    if (c['has_room'] == true) {
      return stored == 0 ? 'ok' : (fill > 0 ? 'fixable' : 'unfixable');
    }
    return stored > 0 ? 'held' : '';
  }

  /// A room exists and stored messages can still be added at its end.
  static bool _isMissing(Map<String, dynamic> c) => _state(c) == 'fixable';

  /// Missing messages can't be added any more (or the chat can't be created).
  static bool _isUnfixable(Map<String, dynamic> c) => _state(c) == 'unfixable';

  /// The phone sent history, but the chat got no room (not among the first chats).
  static bool _isHeld(Map<String, dynamic> c) => _state(c) == 'held';

  static bool _selectable(Map<String, dynamic> c) =>
      _isMissing(c) || _isHeld(c);

  String _loginName(Map<String, dynamic> l) {
    final n = (l['name'] as String?) ?? '';
    return n.isNotEmpty ? n : '${l['id']}';
  }

  /// [mode]: "missing" or "held"; or exactly [chats].
  Future<void> _catchup({String? mode, List<String>? chats}) async {
    final login = _login;
    final account = login == null || _logins.length < 2
        ? ''
        : ' of ${_loginName(login)}';
    final what = chats != null
        ? '${chats.length} chosen chats$account'
        : mode == 'held'
        ? 'the chats$account that were held back (only the most recent chats were created at login)'
        : 'the missing messages at the end of the chats$account where the import stopped early';
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: Text(
          chats != null
              ? 'Copy chosen chats?'
              : mode == 'held'
              ? 'Copy held-back chats?'
              : 'Copy missing messages?',
        ),
        content: Text(
          'The bridge restarts and copies $what. Nothing else is copied. '
          'Your own messages go at the speed tjena allows, so this can take a while; '
          'keep the bridge running. You can stop it any time with "Stop copying".\n\n'
          'Messages can only be added after the newest message already in a chat. If someone wrote '
          'in a chat after the import stopped, the gap before that message stays.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(c, true),
            child: const Text('Copy'),
          ),
        ],
      ),
    );
    if (ok != true || !mounted) return;
    setState(() => _catchingUp = true);
    final messenger = ScaffoldMessenger.of(context);
    try {
      final r = await Native.historyCatchup(
        mode: chats == null ? mode : null,
        login: _logins.length > 1 ? (login?['id'] as String?) : null,
        chats: chats,
      );
      final n = (r['chats'] as num? ?? 0).toInt();
      messenger.showSnackBar(
        SnackBar(
          content: Text(
            n == 0
                ? 'Nothing to copy.'
                : 'Copying ${r['stored']} messages in $n chats. This screen shows the progress.',
          ),
        ),
      );
      if (mounted) {
        setState(() {
          _selecting = false;
          _selected.clear();
        });
      }
    } catch (e) {
      messenger.showSnackBar(
        SnackBar(
          content: Text('Failed: ${e is PlatformException ? e.message : e}'),
        ),
      );
    } finally {
      if (mounted) setState(() => _catchingUp = false);
      await Future.delayed(const Duration(seconds: 3));
      _load();
    }
  }

  Future<void> _stop() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: const Text('Stop copying?'),
        content: const Text(
          'The bridge restarts and stops creating and filling chats from history, for all accounts. '
          'Chats already copied stay. What wasn\'t copied stays stored on this phone: you can copy it '
          'later with the buttons here. New messages keep arriving as usual.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(c, true),
            child: const Text('Stop'),
          ),
        ],
      ),
    );
    if (ok != true || !mounted) return;
    setState(() => _stopping = true);
    final messenger = ScaffoldMessenger.of(context);
    try {
      final r = await Native.historyStop();
      messenger.showSnackBar(
        SnackBar(
          content: Text(
            'Stopped. ${r['stopped'] ?? 0} chats were still waiting; they can be copied later.',
          ),
        ),
      );
    } catch (e) {
      messenger.showSnackBar(
        SnackBar(
          content: Text('Failed: ${e is PlatformException ? e.message : e}'),
        ),
      );
    } finally {
      if (mounted) setState(() => _stopping = false);
      await Future.delayed(const Duration(seconds: 3));
      _load();
    }
  }

  @override
  Widget build(BuildContext context) {
    final logins = _logins;
    if (_tab >= logins.length) _tab = 0;
    return DefaultTabController(
      key: ValueKey(logins.map((l) => l['id']).join(',')),
      length: logins.length > 1 ? logins.length : 1,
      initialIndex: logins.length > 1 ? _tab : 0,
      child: _scaffold(context, logins),
    );
  }

  Widget _scaffold(BuildContext context, List<Map<String, dynamic>> logins) {
    final selectedCount = _selected.length;
    return Scaffold(
      bottomNavigationBar: _selecting
          ? SafeArea(
              child: Padding(
                padding: const EdgeInsets.all(12),
                child: Row(
                  children: [
                    TextButton(
                      onPressed: () => setState(() {
                        _selecting = false;
                        _selected.clear();
                      }),
                      child: const Text('Cancel'),
                    ),
                    const Spacer(),
                    FilledButton.icon(
                      onPressed: selectedCount == 0 || _catchingUp
                          ? null
                          : () => _catchup(chats: _selected.toList()),
                      icon: const Icon(Icons.download),
                      label: Text('Copy $selectedCount chats'),
                    ),
                  ],
                ),
              ),
            )
          : null,
      appBar: AppBar(
        bottom: logins.length > 1
            ? TabBar(
                isScrollable: logins.length > 2,
                onTap: (i) => setState(() {
                  _tab = i;
                  _selecting = false;
                  _selected.clear();
                }),
                tabs: [for (final l in logins) Tab(text: _loginName(l))],
              )
            : null,
        title: Text(
          _appVersion.isEmpty
              ? 'History import'
              : 'History import · v$_appVersion',
        ),
        actions: [
          IconButton(
            tooltip: 'Copy summary',
            onPressed: _info == null ? null : _copy,
            icon: const Icon(Icons.copy),
          ),
          IconButton(onPressed: _load, icon: const Icon(Icons.refresh)),
        ],
      ),
      body: _info == null
          ? Center(
              child: _error != null
                  ? Padding(
                      padding: const EdgeInsets.all(24),
                      child: ErrorText(
                        'Could not read the import state: $_error',
                      ),
                    )
                  : const CircularProgressIndicator(),
            )
          : RefreshIndicator(onRefresh: _load, child: _body(context)),
    );
  }

  Widget _body(BuildContext context) {
    final t = Theme.of(context);
    final log = _log;
    final progress = (log['progress'] as Map?)?.cast<String, dynamic>() ?? {};
    final chunks = ((log['chunks'] as List?) ?? []).cast<Map>();
    final lastChunk = chunks.isEmpty
        ? null
        : chunks.last.cast<String, dynamic>();
    final lastChunkTime = DateTime.tryParse(
      lastChunk?['time'] as String? ?? '',
    );
    final phonePct = [
      for (final v in progress.values) (v as num).toInt(),
    ].fold<int>(0, (a, b) => b > a ? b : a);
    final pending = _sum('pending_chunks');
    final roomsTarget = (log['rooms_target'] as num?)?.toInt() ?? 0;
    final rooms = _sum('rooms');
    final roomsDone = log['rooms_done'] == true;
    final creating = log['rooms_creating'] == true;
    final copiedChats = (log['backfill_chats'] as num?)?.toInt() ?? 0;
    final notCopied = _sum('not_copied');
    final rateLimited = (log['rate_limited'] as num?)?.toInt() ?? 0;
    final errors = ((log['recent_errors'] as List?) ?? []).cast<Map>();
    final waiting = (_pace['sends_waiting'] as num?)?.toInt() ?? 0;
    final paceHits = (_pace['rate_limit_hits'] as num?)?.toInt() ?? 0;

    // Phase 1: the phone sends history; phase 2: rooms are created and filled.
    final phoneDone =
        creating ||
        roomsDone ||
        (phonePct >= 100 && pending == 0) ||
        (lastChunk == null && _sum('conversations') > 0 && pending == 0);
    String phoneText;
    if (lastChunk == null && _sum('conversations') > 0) {
      phoneText =
          'Received earlier: history for ${_sum('conversations')} chats '
          '(details are no longer in the log)'
          '${pending > 0 ? ' · $pending waiting to be read' : ''}';
    } else if (lastChunk == null) {
      phoneText = log['found'] == true
          ? 'Nothing received yet. Keep WhatsApp open on your phone.'
          : 'No bridge log found yet.';
    } else {
      final since = lastChunkTime == null
          ? ''
          : ' · last piece ${_ago(lastChunkTime)}';
      phoneText =
          '${chunks.length} pieces received, ${log['saved'] ?? 0} messages stored$since'
          '${pending > 0 ? ' · $pending waiting to be read' : ''}';
    }
    String roomsText;
    if (!creating) {
      roomsText = phoneDone
          ? 'Starting…'
          : 'Starts 5 minutes after the last piece from your phone (10 with "Lots").';
    } else {
      roomsText =
          '$rooms of $roomsTarget chats created · $copiedChats filled'
          '${roomsDone ? '' : ' · working…'}';
    }

    return ListView(
      padding: const EdgeInsets.only(bottom: 24),
      children: [
        Padding(
          padding: const EdgeInsets.all(12),
          child: Card(
            child: Padding(
              padding: const EdgeInsets.all(16),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  _Phase(
                    title: '1. History from your phone',
                    text: phoneText,
                    value: phoneDone
                        ? 1
                        : (phonePct > 0 ? phonePct / 100 : null),
                    done: phoneDone,
                    active: lastChunk != null || !phoneDone,
                  ),
                  const SizedBox(height: 16),
                  _Phase(
                    title: '2. Chats created and filled',
                    text: roomsText,
                    value: !creating
                        ? 0
                        : (roomsDone
                              ? 1
                              : (roomsTarget == 0
                                    ? null
                                    : rooms / roomsTarget)),
                    done: roomsDone,
                    active: creating,
                  ),
                  if (!_running && creating) ...[
                    const SizedBox(height: 12),
                    Text(
                      'Import finished.',
                      style: t.textTheme.titleSmall?.copyWith(
                        color: Colors.green,
                      ),
                    ),
                  ],
                ],
              ),
            ),
          ),
        ),
        if (_dp['paused'] == true)
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
            child: Card(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  ListTile(
                    leading: Icon(Icons.bolt, color: t.colorScheme.primary),
                    title: const Text('Fast import: double puppeting paused'),
                    subtitle: Text(
                      'Your own messages are sent as "you on WhatsApp" for now, so tjena\'s speed limit '
                      'doesn\'t slow the import down. Double puppeting comes back by itself when the import '
                      'is done (at the latest 4 hours after it started)'
                      '${_dp['pending'] != null ? '. Now: ${_dp['pending']}.' : '.'}',
                    ),
                  ),
                  Padding(
                    padding: const EdgeInsets.fromLTRB(8, 0, 8, 8),
                    child: TextButton(
                      onPressed: _resumeDP,
                      child: const Text('Turn double puppeting back on now'),
                    ),
                  ),
                ],
              ),
            ),
          ),
        if (waiting > 0 || paceHits > 0)
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
            child: Card(
              child: ListTile(
                leading: Icon(
                  waiting > 0 ? Icons.hourglass_top : Icons.speed,
                  color: t.colorScheme.primary,
                ),
                title: Text(
                  waiting > 0
                      ? 'Sending slowly: $waiting messages waiting'
                      : 'Sending is paced to tjena\'s limit',
                ),
                subtitle: Text(
                  'tjena allows your own messages only at a certain speed. The bridge app waits and retries '
                  'instead of skipping them (limit reached $paceHits times, '
                  '${_pace['rate_limit_retried'] ?? 0} retried here, '
                  '${_pace['rate_limit_handed_back'] ?? 0} sent back to the bridge to retry later).',
                ),
              ),
            ),
          ),
        if (_running)
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
            child: OutlinedButton.icon(
              onPressed: _stopping ? null : _stop,
              icon: _stopping
                  ? const SizedBox(
                      width: 18,
                      height: 18,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Icon(Icons.stop_circle_outlined),
              label: const Text('Stop copying'),
            ),
          ),
        ..._accountSection(context, rateLimited),
        const SectionTitle('Numbers'),
        _Num('Messages stored from your phone', '${log['saved'] ?? 0}'),
        _Num('Messages copied into chats', '${_sum('bridged_messages')}'),
        _Num(
          'Not copied, can still be added',
          '${_sum('fillable')}',
          bad: _sum('fillable') > 0,
        ),
        _Num(
          'Not copied, can\'t be added any more',
          '${notCopied - _sum('fillable')}',
        ),
        _Num(
          'Waiting for a chat (not in the first chats)',
          '${_sum('stored_messages') - notCopied}',
        ),
        _Num(
          'Skipped message types (calls, system notices…)',
          '${log['ignored'] ?? 0}',
        ),
        _Num(
          'Unreadable messages',
          '${log['parse_dropped'] ?? 0}',
          bad: ((log['parse_dropped'] as num?) ?? 0) > 0,
        ),
        _Num(
          'Rate-limit refusals from tjena',
          '$rateLimited',
          bad: rateLimited > 0,
        ),
        _Num(
          'Messages that failed to send',
          '${log['send_failed'] ?? 0}',
          bad: ((log['send_failed'] as num?) ?? 0) > 0,
        ),
        for (final e in progress.entries)
          _Num('Phone progress (${_typeName(e.key)})', '${e.value}%'),
        const SectionTitle('Chats'),
        SwitchListTile(
          dense: true,
          title: const Text('Only chats with problems'),
          value: _onlyProblems,
          onChanged: (v) => setState(() => _onlyProblems = v),
        ),
        if (_login != null) ..._chatTiles(context, _login!),
        if (errors.isNotEmpty) ...[
          const SectionTitle('Errors'),
          for (final e in errors.reversed.take(20))
            ListTile(
              dense: true,
              leading: Icon(
                Icons.error_outline,
                color: t.colorScheme.error,
                size: 18,
              ),
              title: Text('${e['msg']}'),
              subtitle: Text(
                [
                  _time(e['time'] as String?),
                  if ((e['chat'] as String?)?.isNotEmpty == true)
                    _short(e['chat'] as String),
                  if ((e['error'] as String?)?.isNotEmpty == true) e['error'],
                ].join(' · '),
              ),
            ),
        ],
        for (final err in ((_info?['errors'] as List?) ?? []).cast<String>())
          Padding(padding: const EdgeInsets.all(16), child: ErrorText(err)),
      ],
    );
  }

  /// Warnings and copy actions for the account of the open tab.
  List<Widget> _accountSection(BuildContext context, int rateLimited) {
    final t = Theme.of(context);
    final login = _login;
    if (login == null) return const [];
    final chats = _chatsOf(login);
    final missing = chats.where(_isMissing).toList();
    final held = chats.where(_isHeld).toList();
    final unfixable = chats.where(_isUnfixable).toList();
    final busy = _catchingUp || _stopping;
    int sum(List<Map<String, dynamic>> l, String k) =>
        l.fold(0, (a, c) => a + ((c[k] as num?)?.toInt() ?? 0));
    final lateChats = unfixable
        .where((c) => c['reason'] != 'create_failed')
        .toList();
    final notCreated = unfixable.length - lateChats.length;
    return [
      if (missing.isNotEmpty)
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12),
          child: WarningCard(
            title:
                '${missing.length} chats stopped early · ${sum(missing, 'fillable')} messages can still be added',
            text:
                '${rateLimited > 0 ? 'tjena refused messages for sending too fast ($rateLimited times), and the '
                          'bridge skipped the rest of those chats.' : 'Copying stopped with an error in these chats '
                          '(see "Errors" below).'} '
                'The messages are still stored on this phone: use "Copy missing messages".',
          ),
        ),
      if (unfixable.isNotEmpty)
        Card(
          margin: const EdgeInsets.fromLTRB(12, 8, 12, 0),
          child: ListTile(
            leading: Icon(Icons.block, color: t.colorScheme.outline),
            title: Text(
              '${unfixable.length} chats can\'t be completed · ${sum(lateChats, 'stored')} messages',
            ),
            subtitle: Text(
              [
                if (lateChats.isNotEmpty)
                  '${lateChats.length} got newer messages after the import stopped; the missing ones '
                      'would land before those, so they can\'t be added any more.',
                if (notCreated > 0)
                  '$notCreated could not be created by the bridge (WhatsApp refused them several times). '
                      'They no longer hold up other chats; a new message there creates the chat as usual.',
                'Nothing to do: they don\'t block other chats.',
              ].join(' '),
            ),
          ),
        ),
      if (_runningWhy != null && (missing.isNotEmpty || held.isNotEmpty))
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
          child: Text(
            'Import may still be running: $_runningWhy. Copying now restarts the bridge and copies '
            'only what you choose; other waiting chats are skipped.',
            style: t.textTheme.bodySmall,
          ),
        ),
      const SectionTitle('Copy history'),
      ListTile(
        leading: const Icon(Icons.replay),
        title: const Text('Copy missing messages'),
        subtitle: Text(
          missing.isEmpty
              ? 'Nothing missing at the end of your chats.'
              : '${sum(missing, 'fillable')} messages in ${missing.length} chats that stopped early',
        ),
        enabled: missing.isNotEmpty && !busy,
        onTap: () => _catchup(mode: 'missing'),
      ),
      ListTile(
        leading: const Icon(Icons.playlist_add),
        title: const Text('Copy held-back chats'),
        subtitle: Text(
          held.isEmpty
              ? 'No held-back chats.'
              : '${held.length} chats (${sum(held, 'stored')} messages) that weren\'t among the first chats '
                    'created at login',
        ),
        enabled: held.isNotEmpty && !busy,
        onTap: () => _catchup(mode: 'held'),
      ),
      ListTile(
        leading: const Icon(Icons.checklist),
        title: const Text('Choose chats…'),
        subtitle: const Text(
          'Pick chats in the list below, then "Copy" at the bottom',
        ),
        enabled: (missing.isNotEmpty || held.isNotEmpty) && !busy,
        onTap: () => setState(() {
          _selecting = true;
          _onlyProblems = false;
        }),
      ),
    ];
  }

  List<Widget> _chatTiles(BuildContext context, Map<String, dynamic> login) {
    final chats = _chatsOf(login).where((c) {
      if (_selecting) return _selectable(c);
      final problem = _isMissing(c) || _isUnfixable(c);
      return !_onlyProblems || problem;
    }).toList();
    return [
      if (_selecting)
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 4, 16, 4),
          child: Row(
            children: [
              Expanded(
                child: Text(
                  'Chats that can be copied (${chats.length})',
                  style: Theme.of(context).textTheme.labelLarge,
                ),
              ),
              TextButton(
                onPressed: () => setState(() {
                  final all = chats.map((c) => c['jid'] as String).toSet();
                  if (_selected.containsAll(all)) {
                    _selected.removeAll(all);
                  } else {
                    _selected.addAll(all);
                  }
                }),
                child: const Text('All / none'),
              ),
            ],
          ),
        ),
      if (chats.isEmpty) const ListTile(dense: true, title: Text('No chats')),
      for (final c in chats)
        _selecting
            ? CheckboxListTile(
                dense: true,
                value: _selected.contains(c['jid']),
                onChanged: (v) => setState(() {
                  final jid = c['jid'] as String;
                  v == true ? _selected.add(jid) : _selected.remove(jid);
                }),
                title: Text(_chatName(c)),
                subtitle: Text(_chatText(c)),
              )
            : ListTile(
                dense: true,
                leading: _chatIcon(context, c),
                title: Text(_chatName(c)),
                subtitle: Text(_chatText(c)),
              ),
    ];
  }

  String _chatName(Map<String, dynamic> c) =>
      (c['name'] as String?)?.isNotEmpty == true
      ? c['name'] as String
      : _short(c['jid'] as String);

  /// Fixable: orange "repair" symbol. Can't be fixed: grey "blocked"
  /// symbol (nothing to do). Otherwise done or waiting.
  Widget _chatIcon(BuildContext context, Map<String, dynamic> c) {
    switch (_state(c)) {
      case 'fixable':
        return const Icon(Icons.build_circle_outlined, color: Colors.orange);
      case 'unfixable':
        return Icon(Icons.block, color: Theme.of(context).colorScheme.outline);
      case 'ok':
        return const Icon(Icons.check_circle_outline, color: Colors.green);
    }
    return const Icon(Icons.schedule, color: Colors.grey);
  }

  String _chatText(Map<String, dynamic> c) {
    final stored = ((c['stored'] as num?) ?? 0).toInt();
    final parts = <String>[];
    if (c['has_room'] == true) {
      parts.add('${c['bridged']} in chat');
      if (stored > 0) {
        final fill = ((c['fillable'] as num?) ?? 0).toInt();
        parts.add(
          fill > 0
              ? '$stored not copied ($fill can be added)'
              : '$stored not copied (can\'t be added any more)',
        );
      }
      if (c['rate_limit'] == true) {
        parts.add('stopped: rate limit');
      } else if (c['failed'] == true) {
        parts.add('stopped: error');
      }
    } else {
      final tries = ((c['create_failed'] as num?) ?? 0).toInt();
      parts.add(
        _isUnfixable(c)
            ? 'can\'t be created (failed $tries times) · $stored stored · created when a new message arrives'
            : stored > 0
            ? 'held back · $stored stored, copied when a new message arrives or with "Copy held-back chats"'
            : 'no chat yet',
      );
    }
    final ts = (c['last_msg_ts'] as num?)?.toInt() ?? 0;
    if (ts > 0) {
      parts.add(
        'last message ${_ago(DateTime.fromMillisecondsSinceEpoch(ts * 1000))}',
      );
    }
    return parts.join(' · ');
  }

  void _copy() {
    final log = _log;
    final summary = {
      'logins': [
        for (final l in _logins)
          {
            for (final k in [
              'rooms_pending',
              'pending_chunks',
              'conversations',
              'conversations_rooms',
              'stored_messages',
              'not_copied',
              'fillable',
              'blocked_chats',
              'rooms',
              'bridged_messages',
            ])
              k: l[k],
            'problem_chats': (l['chats'] as List)
                .cast<Map>()
                .where(
                  (c) =>
                      c['failed'] == true ||
                      (c['has_room'] == true &&
                          ((c['stored'] as num?) ?? 0) > 0),
                )
                .map(
                  (c) => {
                    'bridged': c['bridged'],
                    'stored': c['stored'],
                    'fillable': c['fillable'],
                    'newer_in_room':
                        ((c['room_newest'] as num?) ?? 0) >
                        ((c['last_msg_ts'] as num?) ?? 0),
                    'rate_limit': c['rate_limit'],
                  },
                )
                .toList(),
          },
      ],
      'log': {
        for (final k in [
          'progress',
          'saved',
          'save_failed',
          'ignored',
          'parse_dropped',
          'rooms_creating',
          'rooms_target',
          'rooms_done',
          'backfill_chats',
          'backfill_messages',
          'backfill_failed',
          'send_failed',
          'rate_limited',
        ])
          k: log[k],
        'chunks': log['chunks'],
        'errors': [
          for (final e in ((log['recent_errors'] as List?) ?? []).cast<Map>())
            '${e['msg']}: ${e['error'] ?? ''}',
        ],
      },
      'app_version': _appVersion,
      'fast_import': {
        for (final k in ['paused', 'import_done', 'pending', 'new_logins'])
          k: _dp[k],
      },
      'pace': {
        for (final e in _pace.entries)
          if (e.key != 'last_rate_limit') e.key: e.value,
      },
    };
    Clipboard.setData(
      ClipboardData(text: const JsonEncoder.withIndent(' ').convert(summary)),
    );
    toast(context, 'Summary copied (no names or numbers)');
  }

  static String _typeName(String t) => switch (t) {
    'INITIAL_BOOTSTRAP' => 'first chats',
    'RECENT' => 'recent',
    'FULL' => 'full history',
    _ => t.toLowerCase(),
  };

  static String _short(String jid) => jid.split('@').first;

  static String _time(String? iso) {
    final d = DateTime.tryParse(iso ?? '')?.toLocal();
    if (d == null) return '';
    String two(int n) => n.toString().padLeft(2, '0');
    return '${two(d.hour)}:${two(d.minute)}:${two(d.second)}';
  }

  static String _ago(DateTime d) {
    final s = DateTime.now().difference(d);
    if (s.inSeconds < 60) return '${s.inSeconds.clamp(0, 59)} s ago';
    if (s.inMinutes < 60) return '${s.inMinutes} min ago';
    if (s.inHours < 48) return '${s.inHours} h ago';
    return '${s.inDays} days ago';
  }
}

class _Phase extends StatelessWidget {
  final String title;
  final String text;
  final double? value; // null = indeterminate
  final bool done;
  final bool active;
  const _Phase({
    required this.title,
    required this.text,
    required this.value,
    required this.done,
    required this.active,
  });

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Icon(
              done ? Icons.check_circle : Icons.radio_button_unchecked,
              size: 18,
              color: done ? Colors.green : t.colorScheme.onSurfaceVariant,
            ),
            const SizedBox(width: 8),
            Expanded(child: Text(title, style: t.textTheme.titleSmall)),
          ],
        ),
        const SizedBox(height: 6),
        LinearProgressIndicator(value: done ? 1 : (active ? value : 0)),
        const SizedBox(height: 4),
        Text(text, style: t.textTheme.bodySmall),
      ],
    );
  }
}

class _Num extends StatelessWidget {
  final String label;
  final String value;
  final bool bad;
  const _Num(this.label, this.value, {this.bad = false});

  @override
  Widget build(BuildContext context) => ListTile(
    dense: true,
    title: Text(label),
    trailing: Text(
      value,
      style: TextStyle(
        fontWeight: FontWeight.w600,
        color: bad ? Theme.of(context).colorScheme.error : null,
      ),
    ),
  );
}
