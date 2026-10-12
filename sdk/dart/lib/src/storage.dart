import 'dart:convert';

import 'core.dart';

/// One bucket's files.
class BucketClient {
  final Transport _t;
  final String id;
  BucketClient(this._t, this.id);

  String _path(String path) => '${Uri.encodeComponent(id)}/${path.split('/').map(Uri.encodeComponent).join('/')}';

  /// Uploads up to 50 MB; upsert overwrites.
  Future<Map<String, dynamic>> upload(String path, List<int> bytes, {String? contentType, bool upsert = false}) async {
    final res = await _t.send('POST', '/storage/v1/object/${_path(path)}', bytes: bytes, headers: {
      if (contentType != null) 'Content-Type': contentType,
      if (upsert) 'x-upsert': 'true',
    });
    return _decode(res.body);
  }

  /// A file's bytes, as the caller's policies allow.
  Future<List<int>> download(String path, {String? range}) async {
    final res = await _t.send('GET', '/storage/v1/object/${_path(path)}', headers: {if (range != null) 'Range': range});
    return res.bodyBytes;
  }

  Future<Map<String, dynamic>> info(String path) async => ((await _t.json('GET', '/storage/v1/object/info/${_path(path)}')) as Map).cast<String, dynamic>();

  /// Files and folders under a prefix.
  Future<({List<Map<String, dynamic>> items, String? nextCursor})> list({String? prefix, String? cursor, int? limit}) async {
    final r = (await _t.json('GET', '/storage/v1/list/${Uri.encodeComponent(id)}', query: {'prefix': prefix, 'cursor': cursor, 'limit': limit})) as Map;
    return (items: ((r['items'] as List?) ?? const []).cast<Map<String, dynamic>>(), nextCursor: r['next_cursor'] as String?);
  }

  /// Deletes files; returns how many.
  Future<int> remove(List<String> paths) async {
    final r = await _t.json('DELETE', '/storage/v1/object/${Uri.encodeComponent(id)}', body: {'paths': paths});
    return r is List ? r.length : 0;
  }

  Future<void> move(String from, String to, {String? toBucket}) async {
    await _t.json('POST', '/storage/v1/object/move', body: {'bucket': id, 'from': from, 'to': to, if (toBucket != null) 'to_bucket': toBucket});
  }

  /// A public bucket's file URL.
  Uri publicUrl(String path) => Uri.parse('${_t.url}/storage/v1/public/${_path(path)}');

  /// A download URL that works without a session until it expires.
  Future<Uri> signedUrl(String path, {Duration expiresIn = const Duration(hours: 1)}) async {
    final r = (await _t.json('POST', '/storage/v1/object/sign/${_path(path)}', body: {'expires_in': expiresIn.inSeconds})) as Map;
    return Uri.parse(r['signed_url'] as String);
  }
}

/// client.storage.
class StorageClient {
  final Transport _t;
  StorageClient(this._t);

  BucketClient bucket(String id) => BucketClient(_t, id);

  // With the secret key:
  Future<List<Map<String, dynamic>>> listBuckets() async => ((await _t.json('GET', '/storage/v1/bucket')) as List).cast<Map<String, dynamic>>();

  Future<Map<String, dynamic>> createBucket(String id, {bool public = false, int? fileSizeLimit, List<String>? allowedMimeTypes}) async =>
      ((await _t.json('POST', '/storage/v1/bucket', body: {
        'id': id,
        'public': public,
        if (fileSizeLimit != null) 'file_size_limit': fileSizeLimit,
        if (allowedMimeTypes != null) 'allowed_mime_types': allowedMimeTypes,
      })) as Map)
          .cast<String, dynamic>();
}

Map<String, dynamic> _decode(String body) => body.isEmpty ? const {} : (jsonDecode(body) as Map).cast<String, dynamic>();
