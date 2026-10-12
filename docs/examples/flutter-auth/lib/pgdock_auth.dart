// A small client for a PGDock project's auth and data APIs (V4 §4.3).
import 'dart:convert';
import 'dart:math';

import 'package:crypto/crypto.dart';
import 'package:flutter_web_auth_2/flutter_web_auth_2.dart';
import 'package:http/http.dart' as http;

class Session {
  Session(this.accessToken, this.refreshToken, this.user);
  final String accessToken;
  final String refreshToken;
  final Map<String, dynamic> user;

  factory Session.fromJson(Map<String, dynamic> j) =>
      Session(j['access_token'] as String, j['refresh_token'] as String,
          (j['user'] as Map).cast<String, dynamic>());
}

class AuthError implements Exception {
  AuthError(this.code, this.message);
  final String code;
  final String message;
  @override
  String toString() => message;
}

class PgDock {
  /// [url] is the project's API (https://<ref>.<domain>); [publishableKey]
  /// is the project's publishable key.
  PgDock(this.url, this.publishableKey);
  final String url;
  final String publishableKey;
  Session? session;

  Map<String, String> get _headers => {
        'apikey': publishableKey,
        'Content-Type': 'application/json',
        if (session != null) 'Authorization': 'Bearer ${session!.accessToken}',
      };

  Future<Map<String, dynamic>> _post(String path, Map<String, dynamic> body) async {
    final r = await http.post(Uri.parse('$url$path'), headers: _headers, body: jsonEncode(body));
    final j = r.body.isEmpty ? <String, dynamic>{} : (jsonDecode(r.body) as Map).cast<String, dynamic>();
    if (r.statusCode >= 300) {
      final e = (j['error'] as Map?)?.cast<String, dynamic>() ?? {};
      throw AuthError('${e['code'] ?? r.statusCode}', '${e['message'] ?? r.body}');
    }
    return j;
  }

  /// Sends a sign-in code by WhatsApp (or "sms"). Nigerian numbers may be
  /// local (0803…) or international (+234803…).
  Future<void> sendCode(String phone, {String channel = 'whatsapp'}) =>
      _post('/auth/v1/signin/otp', {'phone': phone, 'channel': channel});

  /// Checks the 6-digit code and signs in.
  Future<Session> verifyCode(String phone, String code) async {
    session = Session.fromJson(await _post('/auth/v1/verify', {'type': 'sms', 'phone': phone, 'token': code}));
    return session!;
  }

  /// Signs in with an OAuth provider ("google", "apple", …) with PKCE.
  /// [redirect] must be the project's site URL or one of its redirect URLs,
  /// e.g. "com.example.app://login-callback".
  Future<Session> signInWith(String provider, String redirect) async {
    final verifier = _randomString(64);
    final challenge = base64Url.encode(sha256.convert(utf8.encode(verifier)).bytes).replaceAll('=', '');
    final authorize = Uri.parse('$url/auth/v1/authorize').replace(queryParameters: {
      'provider': provider,
      'redirect_to': redirect,
      'code_challenge': challenge,
      'code_challenge_method': 's256',
    });
    final result = await FlutterWebAuth2.authenticate(
        url: authorize.toString(), callbackUrlScheme: Uri.parse(redirect).scheme);
    final back = Uri.parse(result);
    final error = back.queryParameters['error_description'];
    if (error != null) throw AuthError(back.queryParameters['error_code'] ?? 'oauth', error);
    session = Session.fromJson(await _post('/auth/v1/token?grant_type=pkce',
        {'auth_code': back.queryParameters['code'], 'code_verifier': verifier}));
    return session!;
  }

  /// Exchanges the refresh token for new tokens (custom claims are added
  /// again: a changed org shows up here).
  Future<Session> refresh() async {
    session = Session.fromJson(
        await _post('/auth/v1/token?grant_type=refresh_token', {'refresh_token': session!.refreshToken}));
    return session!;
  }

  /// Reads rows from a table through the data API.
  Future<List<Map<String, dynamic>>> select(String table, {String query = ''}) async {
    final r = await http.get(Uri.parse('$url/data/v1/$table${query.isEmpty ? '' : '?$query'}'), headers: _headers);
    final j = (jsonDecode(r.body) as Map).cast<String, dynamic>();
    if (r.statusCode >= 300) {
      final e = (j['error'] as Map).cast<String, dynamic>();
      throw AuthError('${e['code']}', '${e['message']}');
    }
    return (j['data'] as List).map((e) => (e as Map).cast<String, dynamic>()).toList();
  }

  Future<void> signOut() async {
    await _post('/auth/v1/signout', {});
    session = null;
  }

  static String _randomString(int n) {
    const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~';
    final r = Random.secure();
    return List.generate(n, (_) => chars[r.nextInt(chars.length)]).join();
  }
}
