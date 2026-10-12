import 'dart:convert';

import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:pgdock/pgdock.dart';
import 'package:test/test.dart';

void main() {
  test('conditions encode the way the edge parses them', () {
    expect(encodeCondition('done', Op.eq, false), 'done:eq:false');
    expect(encodeCondition('id', Op.in_, [1, 'a,b', 'q"x']), r'id:in:1,"a,b","q\"x"');
    expect(encodeCondition('deleted_at', Op.is_, null), 'deleted_at:is:null');
    expect(encodeCondition('tags', Op.contains, ['a']), 'tags:contains:["a"]');
  });

  test('reads with GET, or the JSON form for not()', () async {
    final calls = <http.Request>[];
    final mock = MockClient((r) async {
      calls.add(r);
      return http.Response(jsonEncode({'data': [{'id': 1}], 'next_cursor': 'c2', 'count': 5}), 200);
    });
    final pgd = PgdockClient('https://k7.api.pgdock.test', 'pgd_pub_x', httpClient: mock);
    final page = await pgd.data.from('todos').select('id,title').eq('done', false).or([const Cond('a', Op.eq, 1), const Cond('b', Op.eq, 2)]).order('created_at', descending: true).count().get();
    expect(page.rows, [{'id': 1}]);
    expect(page.nextCursor, 'c2');
    expect(page.count, 5);
    final r = calls[0];
    expect(r.method, 'GET');
    expect(r.url.path, '/data/v1/todos');
    expect(r.url.queryParametersAll['where'], ['done:eq:false']);
    expect(r.url.queryParameters['or'], 'a:eq:1,b:eq:2');
    expect(r.headers['apikey'], 'pgd_pub_x');

    await pgd.data.from('api.items').select().not(const Cond('x', Op.is_, null)).replica().get();
    final q = calls[1];
    expect(q.method, 'POST');
    expect(q.url.path, '/data/v1/api.items/query');
    expect(jsonDecode(q.body)['where'], {
      'and': [
        {'not': {'column': 'x', 'op': 'is', 'value': null}}
      ]
    });
    expect(q.headers['Read-Replica'], 'allowed');
  });

  test('API errors throw PgdockException', () async {
    final mock = MockClient((_) async => http.Response(jsonEncode({'error': {'code': 'rls_required', 'message': 'enable it', 'request_id': 'req_1'}}), 403));
    final pgd = PgdockClient('https://k7.api.pgdock.test', 'pgd_pub_x', httpClient: mock);
    await expectLater(
        pgd.data.from('todos').select().get(),
        throwsA(isA<PgdockException>().having((e) => e.status, 'status', 403).having((e) => e.code, 'code', 'rls_required').having((e) => e.requestId, 'id', 'req_1')));
  });

  test('refreshes once at a time and sends the new token', () async {
    var refreshes = 0;
    final now = DateTime.now().millisecondsSinceEpoch ~/ 1000;
    final reads = <String?>[];
    final mock = MockClient((r) async {
      switch (r.url.path) {
        case '/auth/v1/signin/password':
          return http.Response(jsonEncode({'access_token': 'old', 'refresh_token': 'r1', 'expires_at': now + 30, 'user': {'id': 'u1'}}), 200);
        case '/auth/v1/token':
          refreshes++;
          await Future<void>.delayed(const Duration(milliseconds: 30));
          return http.Response(jsonEncode({'access_token': 'fresh', 'refresh_token': 'r2', 'expires_at': now + 3600, 'user': {'id': 'u1'}}), 200);
      }
      reads.add(r.headers['Authorization']);
      return http.Response(jsonEncode({'data': []}), 200);
    });
    final pgd = PgdockClient('https://k7.api.pgdock.test', 'pgd_pub_x', httpClient: mock);
    final s = await pgd.auth.signInWithPassword(email: 'a@b.c', password: 'pw');
    expect(s!.userId, 'u1');
    await Future.wait([pgd.data.from('a').select().get(), pgd.data.from('b').select().get()]);
    expect(refreshes, 1);
    expect(reads, ['Bearer fresh', 'Bearer fresh']);
    pgd.dispose();
  });

  test('storage escapes paths', () async {
    final calls = <http.Request>[];
    final mock = MockClient((r) async {
      calls.add(r);
      return http.Response(jsonEncode({'id': '1', 'path': 'u 1/me.png'}), 200);
    });
    final pgd = PgdockClient('https://k7.api.pgdock.test', 'pgd_pub_x', httpClient: mock);
    await pgd.storage.bucket('avatars').upload('u 1/me.png', [1, 2], contentType: 'image/png', upsert: true);
    expect(calls[0].url.path, '/storage/v1/object/avatars/u%201/me.png');
    expect(calls[0].headers['x-upsert'], 'true');
    expect(pgd.storage.bucket('docs').publicUrl('a/b.txt').toString(), 'https://k7.api.pgdock.test/storage/v1/public/docs/a/b.txt');
  });
}
