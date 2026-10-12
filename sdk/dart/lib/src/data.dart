import 'dart:async';
import 'dart:convert';

import 'core.dart';

/// Filter operators.
enum Op { eq, neq, lt, lte, gt, gte, in_, like, ilike, is_, contains, containedBy, search }

String _opName(Op o) => switch (o) {
      Op.in_ => 'in',
      Op.is_ => 'is',
      Op.containedBy => 'contained_by',
      _ => o.name,
    };

/// A condition, or a group (the JSON query form).
sealed class Filter {
  const Filter();
  Object toJson();
}

class Cond extends Filter {
  final String column;
  final Op op;
  final Object? value;
  const Cond(this.column, this.op, this.value);
  @override
  Object toJson() => {'column': column, 'op': _opName(op), 'value': _jsonValue(op, value)};
}

class AnyOf extends Filter {
  final List<Filter> filters;
  const AnyOf(this.filters);
  @override
  Object toJson() => {'or': filters.map((f) => f.toJson()).toList()};
}

class AllOf extends Filter {
  final List<Filter> filters;
  const AllOf(this.filters);
  @override
  Object toJson() => {'and': filters.map((f) => f.toJson()).toList()};
}

class NotF extends Filter {
  final Filter filter;
  const NotF(this.filter);
  @override
  Object toJson() => {'not': filter.toJson()};
}

Object? _jsonValue(Op op, Object? v) {
  if (v is DateTime) return v.toUtc().toIso8601String();
  if (op == Op.in_ && v is! List) return [v];
  return v;
}

String _scalar(Object? v) {
  if (v == null) return 'null';
  if (v is DateTime) return v.toUtc().toIso8601String();
  if (v is Map || v is List) return jsonEncode(v);
  return '$v';
}

String _item(Object? v) {
  final s = _scalar(v);
  return RegExp(r'[",\\]').hasMatch(s) ? '"${s.replaceAllMapped(RegExp(r'[\\"]'), (m) => '\\${m[0]}')}"' : s;
}

/// The URL form of a condition: column:op:value.
String encodeCondition(String column, Op op, Object? value) {
  if (op == Op.in_) {
    final items = value is List ? value : [value];
    return '$column:in:${items.map(_item).join(',')}';
  }
  return '$column:${_opName(op)}:${_scalar(value)}';
}

/// A page of rows.
class Page<T> {
  final List<T> rows;
  final int? count;
  final String? nextCursor;
  const Page(this.rows, this.count, this.nextCursor);
}

mixin _Filters<Self> {
  final List<Cond> _conds = [];
  final List<Filter> _groups = [];

  Self where(String column, Op op, Object? value) {
    _conds.add(Cond(column, op, value));
    return this as Self;
  }

  Self eq(String c, Object? v) => where(c, Op.eq, v);
  Self neq(String c, Object? v) => where(c, Op.neq, v);
  Self gt(String c, Object? v) => where(c, Op.gt, v);
  Self gte(String c, Object? v) => where(c, Op.gte, v);
  Self lt(String c, Object? v) => where(c, Op.lt, v);
  Self lte(String c, Object? v) => where(c, Op.lte, v);
  Self inList(String c, List<Object?> v) => where(c, Op.in_, v);
  Self ilike(String c, String pattern) => where(c, Op.ilike, pattern);
  Self isNull(String c) => where(c, Op.is_, null);

  /// A group of which any condition may hold.
  Self or(List<Filter> any) {
    _groups.add(AnyOf(any));
    return this as Self;
  }

  Self not(Filter f) {
    _groups.add(NotF(f));
    return this as Self;
  }

  bool get _simple => _groups.every((g) => g is AnyOf && g.filters.every((f) => f is Cond && !_scalar(f.value).contains(',')));

  Map<String, dynamic> _urlFilters() => {
        if (_conds.isNotEmpty) 'where': _conds.map((c) => encodeCondition(c.column, c.op, c.value)).toList(),
        if (_groups.isNotEmpty)
          'or': _groups
              .map((g) => (g as AnyOf).filters.map((c) => encodeCondition((c as Cond).column, c.op, c.value)).join(','))
              .toList(),
      };

  Object? _jsonWhere() {
    final all = <Filter>[..._conds, ..._groups];
    return all.isEmpty ? null : AllOf(all).toJson();
  }
}

/// What builders run with.
class DataContext {
  final Transport t;
  final void Function() Function(String schema, String table, void Function() onChange)? watch;
  DataContext(this.t, this.watch);
}

/// A read.
class SelectQuery with _Filters<SelectQuery> {
  final DataContext _ctx;
  final String _schema, _table, _columns;
  final List<String> _order = [];
  int? _limit, _offset;
  String? _cursor, _count;
  bool _replica = false;

  SelectQuery(this._ctx, this._schema, this._table, this._columns);

  SelectQuery order(String column, {bool descending = false}) {
    _order.add('$column:${descending ? 'desc' : 'asc'}');
    return this;
  }

  SelectQuery limit(int n) => this.._limit = n;
  SelectQuery offset(int n) => this.._offset = n;
  SelectQuery cursor(String? c) => this.._cursor = c;
  SelectQuery count([String kind = 'exact']) => this.._count = kind;

  /// May be served by a read replica.
  SelectQuery replica() => this.._replica = true;

  String get _path => '/data/v1/${Uri.encodeComponent(_schema == 'public' ? _table : '$_schema.$_table')}';

  /// Runs the read; fromJson decodes each row (the generated classes' fromJson).
  Future<Page<T>> get<T>([T Function(Map<String, dynamic>)? fromJson]) async {
    final headers = _replica ? {'Read-Replica': 'allowed'} : <String, String>{};
    dynamic r;
    if (_simple) {
      r = await _ctx.t.json('GET', _path,
          query: {
            'select': _columns,
            ..._urlFilters(),
            if (_order.isNotEmpty) 'order': _order.join(','),
            'limit': _limit,
            'offset': _offset,
            'cursor': _cursor,
            'count': _count,
          },
          headers: headers);
    } else {
      r = await _ctx.t.json('POST', '$_path/query',
          body: {
            'select': _columns,
            if (_jsonWhere() != null) 'where': _jsonWhere(),
            if (_order.isNotEmpty) 'order': _order.join(','),
            if (_limit != null) 'limit': _limit,
            if (_offset != null) 'offset': _offset,
            if (_cursor != null) 'cursor': _cursor,
            if (_count != null) 'count': _count,
          },
          headers: headers);
    }
    final data = (r['data'] as List).cast<Map<String, dynamic>>();
    final rows = fromJson == null ? data.cast<T>() : data.map(fromJson).toList();
    return Page<T>(rows, r['count'] as int?, r['next_cursor'] as String?);
  }

  /// Keeps the result current: reads now, and again whenever realtime says
  /// the table changed, after a resync and after a reconnect. Turn realtime
  /// on for the table first. Returns the stop function.
  void Function() live<T>(void Function(Page<T>? page, Object? error) onResult,
      {T Function(Map<String, dynamic>)? fromJson, Duration debounce = const Duration(milliseconds: 100)}) {
    final w = _ctx.watch;
    if (w == null) throw const PgdockException(0, 'realtime_unavailable', 'live() needs realtime');
    Timer? timer;
    var stopped = false;
    var seq = 0;
    void refresh() {
      final mine = ++seq;
      get<T>(fromJson).then((p) {
        if (!stopped && mine == seq) onResult(p, null);
      }, onError: (Object e) {
        if (!stopped && mine == seq) onResult(null, e);
      });
    }

    final unwatch = w(_schema, _table, () {
      timer?.cancel();
      timer = Timer(debounce, refresh);
    });
    refresh();
    return () {
      stopped = true;
      timer?.cancel();
      unwatch();
    };
  }
}

/// An insert, upsert, update or delete.
class WriteQuery with _Filters<WriteQuery> {
  final DataContext _ctx;
  final String _method, _base;
  final Object? _body;
  final Map<String, dynamic> _q = {};
  Object? _key;

  WriteQuery(this._ctx, this._method, this._base, this._body);

  WriteQuery byKey(Object key) => this.._key = key;
  WriteQuery atMost(int n) => this.._q['max_affected'] = n;
  WriteQuery columns(String c) => this.._q['select'] = c;
  WriteQuery minimal() => this.._q['return'] = 'minimal';

  /// Runs the write; returns the written rows and how many changed.
  Future<({int affected, List<Map<String, dynamic>> rows})> run() async {
    if (!_simple) throw const PgdockException(0, 'invalid_filter', 'updates and deletes take where() and or() groups; use data.batch for more');
    final path = _key == null ? _base : '$_base/${Uri.encodeComponent('$_key')}';
    final r = await _ctx.t.json(_method, path, body: _body, query: {..._urlFilters(), ..._q});
    return (affected: (r['affected'] as num).toInt(), rows: ((r['data'] as List?) ?? const []).cast<Map<String, dynamic>>());
  }
}

/// A table or view.
class TableRef {
  final DataContext _ctx;
  final String schema, name;
  TableRef(this._ctx, this.schema, this.name);

  String get _base => '/data/v1/${Uri.encodeComponent(schema == 'public' ? name : '$schema.$name')}';

  SelectQuery select([String columns = '*']) => SelectQuery(_ctx, schema, name, columns);

  /// One row by primary key; throws not_found (404) when hidden or absent.
  Future<Map<String, dynamic>> byKey(Object key, [String columns = '*']) async {
    final r = await _ctx.t.json('GET', '$_base/${Uri.encodeComponent('$key')}', query: {'select': columns});
    return (r['data'] as Map).cast<String, dynamic>();
  }

  WriteQuery insert(Object rows) => WriteQuery(_ctx, 'POST', _base, rows);
  WriteQuery upsert(Object rows, {required List<String> onConflict, bool ignore = false}) {
    final w = WriteQuery(_ctx, 'POST', _base, rows);
    w._q['on_conflict'] = onConflict.join(',');
    if (ignore) w._q['resolution'] = 'ignore';
    return w;
  }

  WriteQuery update(Map<String, dynamic> values) => WriteQuery(_ctx, 'PATCH', _base, values);
  WriteQuery delete() => WriteQuery(_ctx, 'DELETE', _base, null);
}

/// client.data: tables, functions and batches.
class DataClient {
  final DataContext _ctx;
  final String _schema;
  DataClient(this._ctx, [this._schema = 'public']);

  /// A table or view ("schema.table" for another exposed schema).
  TableRef from(String table) {
    final dot = table.indexOf('.');
    return dot > 0 ? TableRef(_ctx, table.substring(0, dot), table.substring(dot + 1)) : TableRef(_ctx, _schema, table);
  }

  DataClient schema(String name) => DataClient(_ctx, name);

  /// Calls a function with named arguments; get: true for STABLE functions.
  Future<dynamic> rpc(String fn, [Map<String, dynamic> args = const {}, bool get = false]) async {
    final path = '/data/v1/rpc/${Uri.encodeComponent(_schema == 'public' ? fn : '$_schema.$fn')}';
    final r = get
        ? await _ctx.t.json('GET', path, query: args.map((k, v) => MapEntry(k, _scalar(v))))
        : await _ctx.t.json('POST', path, body: args);
    return r is Map ? r['data'] : r;
  }

  /// Up to 50 writes in one transaction.
  Future<List<dynamic>> batch(List<Map<String, dynamic>> operations) async {
    final r = await _ctx.t.json('POST', '/data/v1/batch', body: {'operations': operations});
    return r['results'] as List;
  }

  Future<Map<String, dynamic>> health() async => ((await _ctx.t.json('GET', '/data/v1/health')) as Map).cast<String, dynamic>();
}
