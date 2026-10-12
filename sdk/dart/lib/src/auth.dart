import 'dart:async';
import 'dart:convert';
import 'dart:math';

import 'package:crypto/crypto.dart';

import 'core.dart';

/// A signed-in user's tokens and the user.
class Session {
  final String accessToken;
  final String refreshToken;
  final int expiresAt; // seconds since the epoch
  final Map<String, dynamic> user;

  const Session({required this.accessToken, required this.refreshToken, required this.expiresAt, required this.user});

  String get userId => user['id'] as String;
  DateTime get expires => DateTime.fromMillisecondsSinceEpoch(expiresAt * 1000);

  factory Session.fromJson(Map<String, dynamic> j) => Session(
        accessToken: j['access_token'] as String,
        refreshToken: j['refresh_token'] as String,
        expiresAt: (j['expires_at'] as num?)?.toInt() ??
            DateTime.now().millisecondsSinceEpoch ~/ 1000 + ((j['expires_in'] as num?)?.toInt() ?? 3600),
        user: (j['user'] as Map?)?.cast<String, dynamic>() ?? const {},
      );

  Map<String, dynamic> toJson() => {'access_token': accessToken, 'refresh_token': refreshToken, 'expires_at': expiresAt, 'user': user};
}

enum AuthEvent { initialSession, signedIn, signedOut, tokenRefreshed, userUpdated }

/// Where the session is kept between launches. In Flutter, wrap
/// flutter_secure_storage (or shared_preferences); the default keeps it
/// in memory.
abstract class SessionStorage {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
  Future<void> delete(String key);
}

class MemoryStorage implements SessionStorage {
  final _m = <String, String>{};
  @override
  Future<String?> read(String key) async => _m[key];
  @override
  Future<void> write(String key, String value) async => _m[key] = value;
  @override
  Future<void> delete(String key) async => _m.remove(key);
}

/// client.auth: sign-up and sign-in, the session, and the user.
class AuthClient {
  final Transport _t;
  final SessionStorage _storage;
  final String _key;
  final bool autoRefresh;
  final Duration refreshMargin;

  Session? _session;
  Future<Session>? _refreshing;
  Timer? _timer;
  final _events = StreamController<(AuthEvent, Session?)>.broadcast();
  late final Future<void> ready;

  AuthClient(this._t, {SessionStorage? storage, String? storageKey, this.autoRefresh = true, this.refreshMargin = const Duration(seconds: 60)})
      : _storage = storage ?? MemoryStorage(),
        _key = storageKey ?? 'pgdock.auth.${Uri.parse(_t.url).host}' {
    ready = _restore();
  }

  /// Sign-ins, refreshes, updates and sign-outs.
  Stream<(AuthEvent, Session?)> get onAuthStateChange => _events.stream;

  Session? get currentSession => _session;

  Future<void> _restore() async {
    try {
      final raw = await _storage.read(_key);
      if (raw != null) _session = Session.fromJson((jsonDecode(raw) as Map).cast<String, dynamic>());
    } catch (_) {
      _session = null;
    }
    if (_session != null && _expiresSoon(_session!)) {
      try {
        await refreshSession();
      } catch (_) {}
    } else {
      _schedule();
    }
    _events.add((AuthEvent.initialSession, _session));
  }

  bool _expiresSoon(Session s) => s.expires.difference(DateTime.now()) < refreshMargin;

  void _schedule() {
    _timer?.cancel();
    final s = _session;
    if (s == null || !autoRefresh) return;
    var d = s.expires.subtract(refreshMargin).difference(DateTime.now());
    if (d.isNegative) d = Duration.zero;
    _timer = Timer(d, () => refreshSession().catchError((_) => _session ?? s));
  }

  Future<void> _save(Session? s, AuthEvent e) async {
    _session = s;
    try {
      if (s == null) {
        await _storage.delete(_key);
      } else {
        await _storage.write(_key, jsonEncode(s.toJson()));
      }
    } catch (_) {}
    _schedule();
    _events.add((e, s));
  }

  /// The session, refreshed first when it is about to expire.
  Future<Session?> getSession() async {
    await ready;
    final s = _session;
    if (s != null && _expiresSoon(s)) {
      try {
        return await refreshSession();
      } catch (_) {
        return _session;
      }
    }
    return s;
  }

  Future<String?> accessToken() async => (await getSession())?.accessToken;

  /// New tokens. One refresh runs at a time: a refresh token works once,
  /// and presenting it again ends the session.
  Future<Session> refreshSession() {
    final r = _refreshing;
    if (r != null) return r;
    final s = _session;
    if (s == null) return Future.error(const PgdockException(401, 'no_session', 'not signed in'));
    final f = () async {
      try {
        final j = await _t.json('POST', '/auth/v1/token',
            query: {'grant_type': 'refresh_token'}, body: {'refresh_token': s.refreshToken}, noToken: true);
        final next = Session.fromJson((j as Map).cast<String, dynamic>());
        await _save(next, AuthEvent.tokenRefreshed);
        return next;
      } on PgdockException catch (e) {
        if (e.status >= 400 && e.status < 500) await _save(null, AuthEvent.signedOut);
        rethrow;
      } finally {
        _refreshing = null;
      }
    }();
    _refreshing = f;
    return f;
  }

  Future<Session?> _signedIn(String path, Object body, {Map<String, dynamic>? query}) async {
    final j = (await _t.json('POST', path, body: body, query: query, noToken: true)) as Map;
    if (j['access_token'] == null) return null; // a confirmation was sent
    final s = Session.fromJson(j.cast<String, dynamic>());
    await _save(s, AuthEvent.signedIn);
    return s;
  }

  /// An account with an email or phone and a password; null while confirmation is pending.
  Future<Session?> signUp({String? email, String? phone, required String password, Map<String, dynamic>? data, String? redirectTo}) =>
      _signedIn('/auth/v1/signup', {
        if (email != null) 'email': email,
        if (phone != null) 'phone': phone,
        'password': password,
        if (data != null) 'data': data,
        if (redirectTo != null) 'redirect_to': redirectTo,
      });

  Future<Session?> signInWithPassword({String? email, String? phone, required String password}) =>
      _signedIn('/auth/v1/signin/password', {if (email != null) 'email': email, if (phone != null) 'phone': phone, 'password': password});

  Future<Session?> signInAnonymously() => _signedIn('/auth/v1/signup', <String, dynamic>{});

  /// A code by email, SMS or WhatsApp (channel: 'sms' or 'whatsapp').
  Future<void> signInWithOtp({String? email, String? phone, String? channel, bool? createUser}) async {
    await _t.json('POST', '/auth/v1/signin/otp',
        body: {
          if (email != null) 'email': email,
          if (phone != null) 'phone': phone,
          if (channel != null) 'channel': channel,
          if (createUser != null) 'create_user': createUser,
        },
        noToken: true);
  }

  /// Signs in with a code: type 'sms', 'whatsapp' or 'phone_change' with
  /// phone; 'signup', 'magiclink', 'email', 'recovery', … with email.
  Future<Session?> verifyOtp({required String type, String? email, String? phone, required String token}) =>
      _signedIn('/auth/v1/verify', {'type': type, if (email != null) 'email': email, if (phone != null) 'phone': phone, 'token': token});

  Future<void> resetPasswordForEmail(String email, {String? redirectTo}) async {
    await _t.json('POST', '/auth/v1/recover', body: {'email': email, if (redirectTo != null) 'redirect_to': redirectTo}, noToken: true);
  }

  /// The authorize URL for a provider, with PKCE; open it (flutter_web_auth_2),
  /// then call exchangeCode with the returned code and this verifier.
  ({Uri url, String verifier}) oauthUrl(String provider, String redirectTo) {
    final rnd = Random.secure();
    final verifier = base64Url.encode(List<int>.generate(32, (_) => rnd.nextInt(256))).replaceAll('=', '');
    final challenge = base64Url.encode(sha256.convert(utf8.encode(verifier)).bytes).replaceAll('=', '');
    final url = _t.uri('/auth/v1/authorize',
        {'provider': provider, 'redirect_to': redirectTo, 'code_challenge': challenge, 'code_challenge_method': 's256'});
    return (url: url, verifier: verifier);
  }

  Future<Session?> exchangeCode(String code, String verifier) =>
      _signedIn('/auth/v1/token', {'auth_code': code, 'code_verifier': verifier}, query: {'grant_type': 'pkce'});

  /// Adopt tokens from elsewhere (a deep link).
  Future<Session> setSession(String accessToken, String refreshToken) async {
    final u = (await _t.json('GET', '/auth/v1/user', tokenOverride: accessToken)) as Map;
    final exp = _jwtExp(accessToken) ?? DateTime.now().millisecondsSinceEpoch ~/ 1000 + 3600;
    final s = Session(accessToken: accessToken, refreshToken: refreshToken, expiresAt: exp, user: u.cast<String, dynamic>());
    await _save(s, AuthEvent.signedIn);
    return s;
  }

  /// Ends this session ('local'), the others, or all ('global').
  Future<void> signOut({String scope = 'local'}) async {
    final s = await getSession();
    if (s != null) {
      try {
        await _t.json('POST', '/auth/v1/signout', query: {'scope': scope}, body: <String, dynamic>{}, tokenOverride: s.accessToken);
      } on PgdockException catch (e) {
        if (e.status != 401) rethrow;
      }
    }
    if (scope != 'others') await _save(null, AuthEvent.signedOut);
  }

  Future<Map<String, dynamic>> getUser() async => ((await _t.json('GET', '/auth/v1/user')) as Map).cast<String, dynamic>();

  Future<Map<String, dynamic>> updateUser({String? password, String? email, String? phone, Map<String, dynamic>? data}) async {
    final u = ((await _t.json('PATCH', '/auth/v1/user', body: {
      if (password != null) 'password': password,
      if (email != null) 'email': email,
      if (phone != null) 'phone': phone,
      if (data != null) 'data': data,
    })) as Map)
        .cast<String, dynamic>();
    final s = _session;
    if (s != null) await _save(Session(accessToken: s.accessToken, refreshToken: s.refreshToken, expiresAt: s.expiresAt, user: u), AuthEvent.userUpdated);
    return u;
  }

  void dispose() {
    _timer?.cancel();
    _events.close();
  }
}

int? _jwtExp(String token) {
  try {
    final p = token.split('.')[1];
    final claims = jsonDecode(utf8.decode(base64Url.decode(base64Url.normalize(p)))) as Map;
    return (claims['exp'] as num?)?.toInt();
  } catch (_) {
    return null;
  }
}
