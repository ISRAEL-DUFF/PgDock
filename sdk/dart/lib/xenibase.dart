/// Xenibase's client for Dart and Flutter: data, auth, storage and realtime.
///
/// ```dart
/// final xb = XenibaseClient('https://k7f3m2q9.api.xenibase.ng', 'xb_pub_…');
/// await xb.auth.signInWithOtp(phone: '+2348012345678', channel: 'whatsapp');
/// await xb.auth.verifyOtp(type: 'whatsapp', phone: '+2348012345678', token: '123456');
/// final page = await xb.data.from('todos').select('id,title').eq('done', false).limit(20).get(Todo.fromJson);
/// ```
library;

import 'package:http/http.dart' as http;

import 'src/auth.dart';
import 'src/core.dart';
import 'src/data.dart';
import 'src/realtime.dart';
import 'src/storage.dart';

export 'src/auth.dart' show AuthClient, AuthEvent, MemoryStorage, Session, SessionStorage;
export 'src/core.dart' show XenibaseException;
export 'src/data.dart';
export 'src/realtime.dart' show Change, RealtimeChannel, RealtimeClient;
export 'src/storage.dart' show BucketClient, StorageClient;

const version = '0.1.0';

/// A project's client.
class XenibaseClient {
  final String url;
  final String key;
  late final AuthClient auth;
  late final DataClient data;
  late final StorageClient storage;
  late final RealtimeClient realtime;

  /// From the project URL and its publishable key (or, on servers only,
  /// its secret key). Pass a SessionStorage to keep the user signed in
  /// across launches.
  XenibaseClient(String url, this.key, {SessionStorage? sessionStorage, http.Client? httpClient, Map<String, String> headers = const {}, String schema = 'public'})
      : url = url.replaceAll(RegExp(r'/+$'), '') {
    final hc = httpClient ?? http.Client();
    final h = {'X-Client-Info': 'xenibase-dart/$version', ...headers};
    final secret = key.startsWith('xb_sec_');
    final bare = Transport(url: this.url, key: key, http_: hc, headers: h, token: () async => null);
    auth = AuthClient(bare, storage: sessionStorage, autoRefresh: !secret);
    final t = secret ? bare : Transport(url: this.url, key: key, http_: hc, headers: h, token: auth.accessToken);
    realtime = RealtimeClient(this.url, key, t.token);
    auth.onAuthStateChange.listen((e) {
      if (e.$1 != AuthEvent.initialSession && e.$1 != AuthEvent.userUpdated) realtime.setToken(e.$2?.accessToken);
    });
    var n = 0;
    data = DataClient(
        DataContext(t, (schema, table, onChange) {
          n++;
          final ch = realtime.channel('xenibase-live:$schema.$table:$n').onChange(table: table, schema: schema, callback: (_) => onChange()).onResync(onChange).subscribe();
          return () => ch.unsubscribe();
        }),
        schema);
    storage = StorageClient(t);
  }

  void dispose() {
    auth.dispose();
    realtime.close();
  }
}
