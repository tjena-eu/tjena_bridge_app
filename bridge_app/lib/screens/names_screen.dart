import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../native.dart';
import 'widgets.dart';

/// Debug: compares each WhatsApp contact's name in Matrix with the name the
/// bridge's own contact store has for them now, and fixes the ones that went
/// stale (typically a phone number shown instead of the saved contact name).
class NamesScreen extends StatefulWidget {
  const NamesScreen({super.key});
  @override
  State<NamesScreen> createState() => _NamesScreenState();
}

class _NamesScreenState extends State<NamesScreen> {
  Map<String, dynamic>? _info;
  String? _error;
  bool _loading = false;
  bool _fixing = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final info = await Native.namesInfo();
      if (mounted) setState(() => _info = info);
    } catch (e) {
      if (mounted) setState(() => _error = e is PlatformException ? e.message : '$e');
    } finally {
      if (mounted) setState(() => _loading = false);
    }
  }

  List<Map<String, dynamic>> _list(String key) =>
      ((_info?[key] as List?) ?? const []).cast<Map<String, dynamic>>();

  Future<void> _fix() async {
    final n = _list('fixable').length;
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: const Text('Update names?'),
        content: Text(
          'Renames $n contact${n == 1 ? '' : 's'} and their chats to the names WhatsApp has for them. '
          'The bridge stops for a moment while this runs.',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(c, false), child: const Text('Cancel')),
          FilledButton(onPressed: () => Navigator.pop(c, true), child: const Text('Update')),
        ],
      ),
    );
    if (ok != true || !mounted) return;
    setState(() => _fixing = true);
    final messenger = ScaffoldMessenger.of(context);
    try {
      final r = await Native.namesFix();
      final failed = (r['failed'] as List?)?.length ?? 0;
      messenger.showSnackBar(SnackBar(
        content: Text(
          'Updated ${r['contacts'] ?? 0} contacts and ${r['rooms'] ?? 0} chats'
          '${failed > 0 ? ' · $failed failed' : ''}.',
        ),
      ));
    } catch (e) {
      messenger.showSnackBar(SnackBar(content: Text('Failed: ${e is PlatformException ? e.message : e}')));
    } finally {
      if (mounted) setState(() => _fixing = false);
      await _load();
    }
  }

  static const _sourceLabel = {
    'contact': 'saved contact name',
    'business': 'business name',
    'push': 'their WhatsApp profile name',
  };

  Widget _entry(Map<String, dynamic> e, {required bool fixable}) {
    final chats = ((e['chats'] as List?) ?? const []).cast<String>();
    final live = ((e['live'] as List?) ?? const []).cast<String>();
    var current = (e['current'] as String?)?.isNotEmpty == true ? e['current'] as String : '(empty)';
    // Database already right, but Matrix still shows something else.
    if (fixable && current == e['expected'] && live.isNotEmpty) current = live.first.split(': ').last;
    return ListTile(
      dense: true,
      leading: Icon(fixable ? Icons.edit_outlined : Icons.help_outline),
      title: Text(fixable ? '$current  →  ${e['expected']}' : current),
      subtitle: Text([
        if (fixable) 'From ${_sourceLabel[e['source']] ?? e['source']}',
        if (!fixable && (e['phone'] as String?)?.isNotEmpty == true) e['phone'] as String,
        if (chats.isNotEmpty && chats.any((c) => c != e['expected'])) 'Chat: ${chats.join(', ')}',
        if (live.isNotEmpty) 'In tjena: ${live.join(', ')}',
      ].join(' · ')),
    );
  }

  @override
  Widget build(BuildContext context) {
    final fixable = _list('fixable');
    final noName = _list('no_name');
    return Scaffold(
      appBar: AppBar(
        title: const Text('Chat names'),
        actions: [
          IconButton(
            tooltip: 'Check again',
            onPressed: _loading || _fixing ? null : _load,
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      body: _info == null
          ? Center(
              child: _error != null
                  ? Padding(padding: const EdgeInsets.all(24), child: Text(_error!))
                  : const CircularProgressIndicator(),
            )
          : RefreshIndicator(
              onRefresh: _load,
              child: ListView(
                children: [
                  Padding(
                    padding: const EdgeInsets.fromLTRB(16, 16, 16, 8),
                    child: Text(
                      '${_info!['contacts'] ?? 0} contacts checked · ${_info!['ok'] ?? 0} fine · '
                      '${fixable.length} can be updated · ${noName.length} without a known name'
                      '${_info!['live_checked'] == true ? ' · compared with what tjena shows' : ''}',
                    ),
                  ),
                  if ((_info!['live_error'] as String?)?.isNotEmpty == true)
                    Padding(
                      padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
                      child: Text(
                        'Could not compare everything with the server: ${_info!['live_error']}',
                        style: TextStyle(color: Theme.of(context).colorScheme.error),
                      ),
                    ),
                  if (_error != null)
                    Padding(
                      padding: const EdgeInsets.symmetric(horizontal: 16),
                      child: Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
                    ),
                  Padding(
                    padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
                    child: FilledButton.icon(
                      onPressed: fixable.isEmpty || _fixing ? null : _fix,
                      icon: _fixing
                          ? const SizedBox.square(dimension: 18, child: CircularProgressIndicator(strokeWidth: 2))
                          : const Icon(Icons.badge_outlined),
                      label: Text(fixable.isEmpty ? 'All names are up to date' : 'Update ${fixable.length} names'),
                    ),
                  ),
                  if (fixable.isNotEmpty) ...[
                    const SectionTitle('Can be updated'),
                    for (final e in fixable) _entry(e, fixable: true),
                  ],
                  if (noName.isNotEmpty) ...[
                    const SectionTitle('Only the number is known'),
                    const Padding(
                      padding: EdgeInsets.fromLTRB(16, 0, 16, 8),
                      child: Text(
                        'Not saved in your phone\'s contacts and no WhatsApp profile name seen yet. '
                        'The name appears once they write, or after you save them as a contact on the phone '
                        'that runs WhatsApp (then check again here).',
                      ),
                    ),
                    for (final e in noName) _entry(e, fixable: false),
                  ],
                ],
              ),
            ),
    );
  }
}
