import 'dart:async';
import 'dart:convert';

import 'package:web_socket_channel/web_socket_channel.dart';

import 'core.dart';

/// A database change realtime delivered.
class Change {
  final String type; // INSERT, UPDATE, DELETE
  final String schema, table;
  final Map<String, dynamic> record, oldRecord;
  const Change(this.type, this.schema, this.table, this.record, this.oldRecord);

  factory Change.fromJson(Map<String, dynamic> j) => Change(
        j['type'] as String? ?? '',
        j['schema'] as String? ?? '',
        j['table'] as String? ?? '',
        (j['record'] as Map?)?.cast<String, dynamic>() ?? const {},
        (j['old_record'] as Map?)?.cast<String, dynamic>() ?? const {},
      );
}

class _ChangeSub {
  final String event, schema, table;
  final String? filter;
  final void Function(Change) cb;
  int? id;
  _ChangeSub(this.event, this.schema, this.table, this.filter, this.cb);
}

/// A channel: database changes, broadcasts and presence on a topic.
class RealtimeChannel {
  final RealtimeClient _rt;
  final String name;
  final bool private;
  final bool broadcastSelf;
  final _changes = <_ChangeSub>[];
  final _broadcasts = <(String, void Function(String event, dynamic payload))>[];
  final _resyncs = <void Function()>[];
  void Function(String status, Object? error)? _status;
  String? joinRef;
  bool joined = false;
  bool _wanted = false;
  Map<String, dynamic>? _tracked;

  RealtimeChannel(this._rt, this.name, {this.private = false, this.broadcastSelf = false});

  String get topic => 'realtime:$name';

  RealtimeChannel onChange({required String table, String schema = 'public', String event = '*', String? filter, required void Function(Change) callback}) {
    _changes.add(_ChangeSub(event, schema, table, filter, callback));
    return this;
  }

  RealtimeChannel onBroadcast(String event, void Function(String event, dynamic payload) callback) {
    _broadcasts.add((event, callback));
    return this;
  }

  /// Changes were dropped or the connection came back: refetch.
  RealtimeChannel onResync(void Function() callback) {
    _resyncs.add(callback);
    return this;
  }

  /// Joins (and rejoins after reconnects); status gets SUBSCRIBED, CHANNEL_ERROR, CLOSED.
  RealtimeChannel subscribe([void Function(String status, Object? error)? status]) {
    _status = status;
    _wanted = true;
    _rt._add(this);
    return this;
  }

  Future<void> unsubscribe() async {
    _wanted = false;
    if (joined) {
      try {
        await _rt._push(topic, 'phx_leave', {}, joinRef);
      } catch (_) {}
    }
    joined = false;
    _rt._remove(this);
  }

  Future<void> send(String event, Object? payload) =>
      _rt._push(topic, 'broadcast', {'type': 'broadcast', 'event': event, 'payload': payload}, joinRef, wait: false);

  Future<void> track(Map<String, dynamic> state) {
    _tracked = state;
    return _rt._push(topic, 'presence', {'type': 'presence', 'event': 'track', 'payload': state}, joinRef, wait: false);
  }

  Future<void> _join(String? token) async {
    if (!_wanted) return;
    joinRef = _rt._nextRef();
    try {
      final resp = await _rt._push(
          topic,
          'phx_join',
          {
            'config': {
              'broadcast': {'self': broadcastSelf, 'ack': false},
              'presence': {'key': ''},
              'postgres_changes': _changes
                  .map((s) => {'event': s.event, 'schema': s.schema, 'table': s.table, if (s.filter != null) 'filter': s.filter})
                  .toList(),
              'private': private,
            },
            if (token != null) 'access_token': token,
          },
          joinRef,
          ref: joinRef);
      final bound = ((resp?['postgres_changes'] as List?) ?? const []);
      for (var i = 0; i < bound.length && i < _changes.length; i++) {
        _changes[i].id = (bound[i]['id'] as num).toInt();
      }
      joined = true;
      if (_tracked != null) await track(_tracked!);
      _status?.call('SUBSCRIBED', null);
    } catch (e) {
      joined = false;
      _status?.call('CHANNEL_ERROR', e);
    }
  }

  void _handle(Map<String, dynamic> m) {
    final p = m['payload'];
    switch (m['event']) {
      case 'postgres_changes':
        final ids = ((p['ids'] as List?) ?? const []).map((e) => (e as num).toInt()).toSet();
        final c = Change.fromJson((p['data'] as Map).cast<String, dynamic>());
        for (final s in _changes) {
          if (s.id != null && ids.contains(s.id)) s.cb(c);
        }
      case 'broadcast':
        final ev = p['event'] as String? ?? '';
        for (final (want, cb) in _broadcasts) {
          if (want == '*' || want == ev) cb(ev, p['payload']);
        }
      case 'system':
        if (p['message'] == 'resync') {
          _resync();
        } else if (p['status'] == 'error') {
          _status?.call('CHANNEL_ERROR', PgdockException(0, 'realtime_error', '${p['message']}'));
        }
      case 'phx_close':
        joined = false;
        if (_wanted) _status?.call('CLOSED', null);
    }
  }

  void _resync() {
    for (final cb in _resyncs) {
      try {
        cb();
      } catch (_) {}
    }
  }
}

/// One WebSocket for every channel; reconnects and rejoins.
class RealtimeClient {
  final String _url, _key;
  final Future<String?> Function() _token;
  WebSocketChannel? _ws;
  StreamSubscription? _sub;
  final _channels = <RealtimeChannel>{};
  final _pending = <String, Completer<Map<String, dynamic>?>>{};
  var _ref = 0;
  Timer? _heartbeat, _reconnect;
  var _attempts = 0;
  var _everConnected = false;
  var _closed = false;
  Future<void>? _connecting;

  RealtimeClient(this._url, this._key, this._token);

  RealtimeChannel channel(String name, {bool private = false, bool broadcastSelf = false}) =>
      RealtimeChannel(this, name, private: private, broadcastSelf: broadcastSelf);

  String _nextRef() => '${++_ref}';

  void _add(RealtimeChannel ch) {
    _channels.add(ch);
    if (_ws != null && _connecting == null) {
      _token().then(ch._join);
    } else {
      _connect();
    }
  }

  void _remove(RealtimeChannel ch) {
    _channels.remove(ch);
    if (_channels.isEmpty) _disconnect();
  }

  Future<void> _connect() {
    if (_closed || _reconnect != null) return Future.value();
    return _connecting ??= () async {
      final u = Uri.parse('${_url.replaceAll(RegExp(r'/+$'), '')}/realtime/v1/websocket');
      final ws = WebSocketChannel.connect(u.replace(scheme: u.scheme == 'https' ? 'wss' : 'ws', queryParameters: {'apikey': _key, 'vsn': '1.0.0'}));
      try {
        await ws.ready;
      } catch (_) {
        _connecting = null;
        _lost();
        return;
      }
      _ws = ws;
      _sub = ws.stream.listen(_onMessage, onDone: _lost, onError: (_) => _lost());
      _attempts = 0;
      final again = _everConnected;
      _everConnected = true;
      _heartbeat = Timer.periodic(const Duration(seconds: 25), (_) => _send({'topic': 'phoenix', 'event': 'heartbeat', 'payload': {}, 'ref': _nextRef()}));
      _connecting = null;
      final t = await _token();
      for (final ch in _channels.toList()) {
        await ch._join(t);
        if (again) ch._resync();
      }
    }();
  }

  void _onMessage(dynamic data) {
    Map<String, dynamic> m;
    try {
      m = (jsonDecode(data as String) as Map).cast<String, dynamic>();
    } catch (_) {
      return;
    }
    if (m['event'] == 'phx_reply') {
      final c = _pending.remove(m['ref']);
      if (c != null) {
        final p = (m['payload'] as Map).cast<String, dynamic>();
        if (p['status'] == 'ok') {
          c.complete((p['response'] as Map?)?.cast<String, dynamic>());
        } else {
          c.completeError(PgdockException(0, 'realtime_error', '${(p['response'] as Map?)?['reason'] ?? p['response']}'));
        }
      }
      return;
    }
    for (final ch in _channels) {
      if (ch.topic == m['topic']) ch._handle(m);
    }
  }

  void _send(Map<String, dynamic> m) => _ws?.sink.add(jsonEncode(m));

  Future<Map<String, dynamic>?> _push(String topic, String event, Object payload, String? joinRef, {bool wait = true, String? ref}) {
    final r = ref ?? _nextRef();
    final m = {'topic': topic, 'event': event, 'payload': payload, 'ref': r, 'join_ref': joinRef};
    if (_ws == null) return Future.error(const PgdockException(0, 'not_connected', 'realtime is not connected'));
    if (!wait) {
      _send(m);
      return Future.value(null);
    }
    final c = Completer<Map<String, dynamic>?>();
    _pending[r] = c;
    _send(m);
    return c.future.timeout(const Duration(seconds: 15), onTimeout: () {
      _pending.remove(r);
      throw PgdockException(0, 'timeout', '$event on $topic got no reply');
    });
  }

  /// Tells joined channels about a new access token.
  void setToken(String? token) {
    for (final ch in _channels) {
      if (ch.joined) _push(ch.topic, 'access_token', {'access_token': token ?? _key}, ch.joinRef, wait: false).catchError((_) => null);
    }
  }

  void _lost() {
    _heartbeat?.cancel();
    _heartbeat = null;
    _sub?.cancel();
    _sub = null;
    _ws = null;
    for (final c in _pending.values) {
      if (!c.isCompleted) c.completeError(const PgdockException(0, 'connection_lost', 'the connection closed'));
    }
    _pending.clear();
    for (final ch in _channels) {
      ch.joined = false;
    }
    if (_closed || _channels.isEmpty) return;
    const delays = [1, 2, 5, 10];
    final d = delays[_attempts < delays.length ? _attempts : delays.length - 1];
    _attempts++;
    _reconnect = Timer(Duration(seconds: d), () {
      _reconnect = null;
      _connect();
    });
  }

  void _disconnect() {
    _reconnect?.cancel();
    _reconnect = null;
    _heartbeat?.cancel();
    _sub?.cancel();
    _ws?.sink.close();
    _ws = null;
    _everConnected = false;
  }

  /// Closes the socket and every channel.
  void close() {
    _closed = true;
    _channels.clear();
    _disconnect();
  }
}
