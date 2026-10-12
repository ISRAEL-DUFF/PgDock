import 'dart:convert';

import 'package:http/http.dart' as http;

/// Every failure the API reports: its status, code and request id. A
/// failure to reach the API is status 0 with code `network_error`.
class PgdockException implements Exception {
  final int status;
  final String code;
  final String message;
  final String? requestId;
  final Object? details;

  const PgdockException(this.status, this.code, this.message, {this.requestId, this.details});

  @override
  String toString() => 'PgdockException($status $code: $message${requestId != null ? ', request $requestId' : ''})';
}

/// What every service shares: the project URL, the key, and the token.
class Transport {
  final String url;
  final String key;
  final http.Client http_;
  final Map<String, String> headers;
  final Future<String?> Function() token;

  Transport({required this.url, required this.key, required this.http_, required this.headers, required this.token});

  Uri uri(String path, [Map<String, dynamic>? query]) {
    final u = Uri.parse(url + path);
    if (query == null || query.isEmpty) return u;
    final qp = <String, dynamic>{};
    query.forEach((k, v) {
      if (v == null) return;
      qp[k] = v is List ? v.map((e) => '$e').toList() : '$v';
    });
    return u.replace(queryParameters: qp);
  }

  /// send makes a request and returns the response; throws PgdockException.
  Future<http.Response> send(
    String method,
    String path, {
    Map<String, dynamic>? query,
    Object? body,
    List<int>? bytes,
    Map<String, String>? headers,
    String? tokenOverride,
    bool noToken = false,
  }) async {
    final h = <String, String>{'apikey': key, ...this.headers, ...?headers};
    final tok = tokenOverride ?? (noToken ? null : await token());
    if (tok != null && tok.isNotEmpty) h['Authorization'] = 'Bearer $tok';
    final req = http.Request(method, uri(path, query));
    req.headers.addAll(h);
    if (body != null) {
      req.headers['Content-Type'] = 'application/json';
      req.body = jsonEncode(body);
    } else if (bytes != null) {
      req.bodyBytes = bytes;
    }
    http.Response res;
    try {
      res = await http.Response.fromStream(await http_.send(req));
    } catch (e) {
      throw PgdockException(0, 'network_error', '$e');
    }
    if (res.statusCode >= 300) throw apiError(res);
    return res;
  }

  /// json makes a request and decodes the JSON answer.
  Future<dynamic> json(
    String method,
    String path, {
    Map<String, dynamic>? query,
    Object? body,
    Map<String, String>? headers,
    String? tokenOverride,
    bool noToken = false,
  }) async {
    final res = await send(method, path,
        query: query, body: body, headers: headers, tokenOverride: tokenOverride, noToken: noToken);
    if (res.body.isEmpty) return null;
    try {
      return jsonDecode(utf8.decode(res.bodyBytes));
    } catch (e) {
      throw PgdockException(res.statusCode, 'invalid_response', 'the answer is not JSON: $e');
    }
  }
}

PgdockException apiError(http.Response res) {
  var code = 'http_${res.statusCode}';
  var message = res.reasonPhrase ?? 'request failed';
  String? requestId = res.headers['x-request-id'];
  Object? details;
  try {
    final b = jsonDecode(utf8.decode(res.bodyBytes));
    final e = b is Map ? (b['error'] ?? b) : null;
    if (e is Map) {
      if (e['code'] is String) code = e['code'];
      if (e['message'] is String) message = e['message'];
      if (e['request_id'] is String) requestId = e['request_id'];
      details = e['details'];
    }
  } catch (_) {}
  return PgdockException(res.statusCode, code, message, requestId: requestId, details: details);
}
