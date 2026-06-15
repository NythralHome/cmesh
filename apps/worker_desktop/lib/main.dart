import 'dart:convert';
import 'dart:math';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

void main(List<String> args) {
  runApp(CMeshWorkerApp(initialInvite: InviteConfig.fromArgs(args)));
}

class CMeshWorkerApp extends StatelessWidget {
  const CMeshWorkerApp({
    super.key,
    required this.initialInvite,
    this.autostartControl = true,
    this.registerProtocolHandler = true,
  });

  final InviteConfig? initialInvite;
  final bool autostartControl;
  final bool registerProtocolHandler;

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'CMesh Worker',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        useMaterial3: true,
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xFF246B5A),
          brightness: Brightness.light,
        ),
        inputDecorationTheme: const InputDecorationTheme(
          border: OutlineInputBorder(),
          isDense: true,
        ),
      ),
      home: WorkerHomePage(
        initialInvite: initialInvite,
        autostartControl: autostartControl,
        registerProtocolHandler: registerProtocolHandler,
      ),
    );
  }
}

class WorkerConfig {
  const WorkerConfig({
    required this.managerUrl,
    required this.joinToken,
    required this.cpu,
    required this.memoryGb,
    required this.diskGb,
    required this.gpuEnabled,
    required this.vramGb,
    required this.benchmark,
    required this.installService,
  });

  final String managerUrl;
  final String joinToken;
  final int cpu;
  final int memoryGb;
  final int diskGb;
  final bool gpuEnabled;
  final int vramGb;
  final bool benchmark;
  final bool installService;

  factory WorkerConfig.empty() {
    return WorkerConfig(
      managerUrl: 'https://cmesh.nythral.com',
      joinToken: '',
      cpu: Platform.numberOfProcessors.clamp(1, 64),
      memoryGb: 8,
      diskGb: 50,
      gpuEnabled: true,
      vramGb: 0,
      benchmark: true,
      installService:
          Platform.isLinux || Platform.isMacOS || Platform.isWindows,
    );
  }

  factory WorkerConfig.fromJson(Map<String, dynamic> json) {
    return WorkerConfig(
      managerUrl:
          json['managerUrl'] as String? ?? json['manager_url'] as String? ?? '',
      joinToken:
          json['joinToken'] as String? ?? json['join_token'] as String? ?? '',
      cpu: json['cpu'] as int? ?? Platform.numberOfProcessors,
      memoryGb: json['memoryGb'] as int? ?? json['memory_gb'] as int? ?? 8,
      diskGb: json['diskGb'] as int? ?? json['disk_gb'] as int? ?? 50,
      gpuEnabled:
          json['gpuEnabled'] as bool? ?? json['gpu_enabled'] as bool? ?? true,
      vramGb: json['vramGb'] as int? ?? json['vram_gb'] as int? ?? 0,
      benchmark: json['benchmark'] as bool? ?? true,
      installService: json['installService'] as bool? ?? true,
    );
  }

  Map<String, dynamic> toJson() {
    return {
      'managerUrl': managerUrl,
      'joinToken': joinToken,
      'cpu': cpu,
      'memoryGb': memoryGb,
      'diskGb': diskGb,
      'gpuEnabled': gpuEnabled,
      'vramGb': vramGb,
      'benchmark': benchmark,
      'installService': installService,
    };
  }

  Map<String, dynamic> toControlJson() {
    return {
      'manager_url': managerUrl,
      'join_token': joinToken,
      'node_name': Platform.localHostname,
      'cpu': cpu,
      'memory_gb': memoryGb,
      'disk_gb': diskGb,
      'gpu_enabled': gpuEnabled,
      'vram_gb': vramGb,
      'benchmark': benchmark,
    };
  }
}

class InviteConfig {
  const InviteConfig({this.managerUrl, this.joinToken});

  final String? managerUrl;
  final String? joinToken;

  static InviteConfig? fromArgs(List<String> args) {
    final candidates = [
      Platform.environment['CMESH_INVITE_URL'],
      ...args,
    ].whereType<String>().where((value) => value.trim().isNotEmpty);
    for (final candidate in candidates) {
      final parsed = fromString(candidate.trim());
      if (parsed != null) return parsed;
    }
    return null;
  }

  static InviteConfig? fromString(String value) {
    final uri = Uri.tryParse(value);
    if (uri == null) return null;
    final query = uri.queryParameters;
    final manager = query['manager'] ?? query['manager_url'];
    final token = query['token'] ?? query['join_token'];
    if ((manager == null || manager.isEmpty) &&
        (token == null || token.isEmpty)) {
      return null;
    }
    return InviteConfig(managerUrl: manager, joinToken: token);
  }
}

class PlatformInviteBridge {
  static const MethodChannel _channel = MethodChannel(
    'cmesh.worker_desktop/invite',
  );

  static Future<InviteConfig?> initialInvite() async {
    if (!Platform.isMacOS) return null;
    try {
      final raw = await _channel.invokeMethod<String>('getInitialInvite');
      if (raw == null || raw.trim().isEmpty) return null;
      return InviteConfig.fromString(raw);
    } on MissingPluginException {
      return null;
    }
  }

  static void setInviteHandler(ValueChanged<InviteConfig> handler) {
    if (!Platform.isMacOS) return;
    _channel.setMethodCallHandler((call) async {
      if (call.method != 'openInvite') return null;
      final raw = call.arguments as String?;
      if (raw == null || raw.trim().isEmpty) return null;
      final invite = InviteConfig.fromString(raw);
      if (invite != null) {
        handler(invite);
      }
      return null;
    });
  }
}

class WorkerProtocolRegistrar {
  Future<WorkerCommandResult> ensureRegistered() async {
    if (Platform.isWindows) {
      return _registerWindows();
    }
    if (Platform.isLinux) {
      return _registerLinux();
    }
    return const WorkerCommandResult(
      exitCode: 0,
      output: 'No registration needed.',
    );
  }

  Future<WorkerCommandResult> _registerWindows() async {
    final executable = Platform.resolvedExecutable;
    final command = '"$executable" "%1"';
    final operations = [
      [
        'add',
        r'HKCU\Software\Classes\cmesh',
        '/ve',
        '/d',
        'URL:CMesh Worker',
        '/f',
      ],
      [
        'add',
        r'HKCU\Software\Classes\cmesh',
        '/v',
        'URL Protocol',
        '/d',
        '',
        '/f',
      ],
      [
        'add',
        r'HKCU\Software\Classes\cmesh\DefaultIcon',
        '/ve',
        '/d',
        '$executable,0',
        '/f',
      ],
      [
        'add',
        r'HKCU\Software\Classes\cmesh\shell\open\command',
        '/ve',
        '/d',
        command,
        '/f',
      ],
    ];
    for (final operation in operations) {
      final result = await Process.run('reg', operation);
      if (result.exitCode != 0) {
        return WorkerCommandResult(
          exitCode: result.exitCode,
          output:
              'Failed to register cmesh:// protocol.\n\n${result.stderr}${result.stdout}',
        );
      }
    }
    return const WorkerCommandResult(
      exitCode: 0,
      output: 'Registered cmesh:// protocol for this Windows user.',
    );
  }

  Future<WorkerCommandResult> _registerLinux() async {
    final executable = Platform.resolvedExecutable;
    final home = Platform.environment['HOME'];
    if (home == null || home.isEmpty) {
      return const WorkerCommandResult(
        exitCode: 1,
        output: 'Cannot register cmesh:// protocol because HOME is not set.',
      );
    }
    final applicationsDir = Directory('$home/.local/share/applications');
    await applicationsDir.create(recursive: true);
    final desktopFile = File(
      '${applicationsDir.path}/com.nythral.cmesh.worker.desktop',
    );
    await desktopFile.writeAsString('''
[Desktop Entry]
Type=Application
Name=CMesh Worker
Comment=Join and control a CMesh worker
Exec="${_escapeDesktopExec(executable)}" %u
Terminal=false
Categories=Network;Utility;
MimeType=x-scheme-handler/cmesh;
''');

    await _runBestEffort('update-desktop-database', [applicationsDir.path]);
    final mime = await Process.run('xdg-mime', [
      'default',
      desktopFile.uri.pathSegments.last,
      'x-scheme-handler/cmesh',
    ]);
    if (mime.exitCode != 0) {
      return WorkerCommandResult(
        exitCode: mime.exitCode,
        output:
            'Created ${desktopFile.path}, but xdg-mime could not set it as the cmesh:// handler.\n\n${mime.stderr}${mime.stdout}',
      );
    }
    return WorkerCommandResult(
      exitCode: 0,
      output: 'Registered cmesh:// protocol using ${desktopFile.path}.',
    );
  }

  Future<void> _runBestEffort(String executable, List<String> args) async {
    try {
      await Process.run(executable, args);
    } on Object {
      // Optional desktop database refresh; xdg-mime is the authoritative step.
    }
  }

  String _escapeDesktopExec(String value) {
    return value.replaceAll(r'\', r'\\').replaceAll('"', r'\"');
  }
}

class WorkerConfigStore {
  Future<File> _file() async {
    final home =
        Platform.environment['HOME'] ??
        Platform.environment['USERPROFILE'] ??
        Directory.current.path;
    final dir = Directory('$home/.cmesh');
    if (!await dir.exists()) {
      await dir.create(recursive: true);
    }
    return File('${dir.path}/worker-desktop.json');
  }

  Future<WorkerConfig> load() async {
    final file = await _file();
    if (!await file.exists()) {
      return WorkerConfig.empty();
    }
    final raw = await file.readAsString();
    return WorkerConfig.fromJson(jsonDecode(raw) as Map<String, dynamic>);
  }

  Future<void> save(WorkerConfig config) async {
    final file = await _file();
    await file.writeAsString(
      const JsonEncoder.withIndent('  ').convert(config.toJson()),
    );
  }
}

class WorkerCommandResult {
  const WorkerCommandResult({
    required this.exitCode,
    required this.output,
    this.json,
  });

  final int exitCode;
  final String output;
  final Object? json;

  bool get ok => exitCode == 0;
}

class WorkerControlTokenStore {
  static String loadOrCreateSync() {
    final configured = Platform.environment['CMESH_WORKER_CONTROL_TOKEN'];
    if (configured != null && configured.trim().isNotEmpty) {
      return configured.trim();
    }

    final file = _file();
    try {
      if (file.existsSync()) {
        final token = file.readAsStringSync().trim();
        if (token.isNotEmpty) return token;
      }
      file.parent.createSync(recursive: true);
      final token = _generateControlToken();
      file.writeAsStringSync(token);
      return token;
    } on Object {
      return _generateControlToken();
    }
  }

  static File _file() {
    final home =
        Platform.environment['HOME'] ??
        Platform.environment['USERPROFILE'] ??
        Directory.current.path;
    return File('$home/.cmesh/worker-control-token');
  }
}

class WorkerRuntimeStatus {
  const WorkerRuntimeStatus({
    required this.running,
    this.pid,
    this.startedAt,
    this.exitCode,
    this.lastError,
    this.logTail = '',
  });

  final bool running;
  final int? pid;
  final DateTime? startedAt;
  final int? exitCode;
  final String? lastError;
  final String logTail;

  factory WorkerRuntimeStatus.fromJson(Map<String, dynamic> json) {
    final startedAtRaw = json['started_at'] as String?;
    return WorkerRuntimeStatus(
      running: json['running'] as bool? ?? false,
      pid: json['pid'] as int?,
      startedAt: startedAtRaw == null ? null : DateTime.tryParse(startedAtRaw),
      exitCode: json['exit_code'] as int?,
      lastError: json['last_error'] as String?,
      logTail: json['log_tail'] as String? ?? '',
    );
  }

  String get label {
    if (running) return 'Running';
    if (lastError != null && lastError!.isNotEmpty) return 'Error';
    if (exitCode != null) return 'Stopped';
    return 'Not running';
  }
}

class WorkerController {
  static final Uri _baseURL = Uri.parse(
    Platform.environment['CMESH_WORKER_CONTROL_URL'] ?? 'http://127.0.0.1:9781',
  );

  final String _controlToken = WorkerControlTokenStore.loadOrCreateSync();
  Process? _controlProcess;

  Future<WorkerCommandResult> ensureRunning() async {
    final health = await _request('GET', '/health', tryStart: false);
    if (health.ok) {
      return health;
    }
    final binary = await _findControlBinary();
    if (binary == null) {
      return WorkerCommandResult(
        exitCode: 1,
        output:
            'Worker control API is not running at $_baseURL.\n\nCould not find the cmesh binary. Set CMESH_WORKER_CONTROL_BIN or build it with:\n  make build',
      );
    }
    try {
      _controlProcess = await Process.start(
        binary,
        ['worker', 'control'],
        environment: {
          ...Platform.environment,
          'CMESH_WORKER_CONTROL_TOKEN': _controlToken,
        },
        mode: ProcessStartMode.detachedWithStdio,
      );
      _controlProcess!.stdout
          .transform(utf8.decoder)
          .listen((_) {}, onError: (_) {});
      _controlProcess!.stderr
          .transform(utf8.decoder)
          .listen((_) {}, onError: (_) {});
    } on Object catch (error) {
      return WorkerCommandResult(
        exitCode: 1,
        output: 'Failed to start worker control API with $binary.\n\n$error',
      );
    }

    final deadline = DateTime.now().add(const Duration(seconds: 8));
    while (DateTime.now().isBefore(deadline)) {
      await Future<void>.delayed(const Duration(milliseconds: 250));
      final ready = await _request('GET', '/health', tryStart: false);
      if (ready.ok) {
        return WorkerCommandResult(
          exitCode: 0,
          output: 'Worker control API started at $_baseURL using $binary',
        );
      }
    }
    return WorkerCommandResult(
      exitCode: 1,
      output: 'Started $binary, but worker control API did not become ready.',
    );
  }

  Future<WorkerCommandResult> install(WorkerConfig config) async {
    final save = await saveConfig(config);
    if (!save.ok) {
      return save;
    }
    return _request('POST', '/v1/start');
  }

  Future<WorkerCommandResult> saveConfig(WorkerConfig config) {
    return _request('PUT', '/v1/config', body: config.toControlJson());
  }

  Future<WorkerCommandResult> serviceAction(String action) {
    if (action == 'status') {
      return _request('GET', '/v1/status');
    }
    if (action == 'disconnect') {
      return _request('POST', '/v1/stop');
    }
    return _request('POST', '/v1/$action');
  }

  Future<WorkerCommandResult> _request(
    String method,
    String path, {
    Map<String, dynamic>? body,
    bool tryStart = true,
  }) async {
    final client = HttpClient()..connectionTimeout = const Duration(seconds: 2);
    try {
      final req = await client.openUrl(method, _baseURL.resolve(path));
      req.headers.contentType = ContentType.json;
      if (path.startsWith('/v1/')) {
        req.headers.set('X-CMesh-Control-Token', _controlToken);
      }
      if (body != null) {
        req.write(jsonEncode(body));
      }
      final resp = await req.close();
      final raw = await utf8.decodeStream(resp);
      final decoded = _decodeResponse(raw);
      final output = _formatResponse(resp.statusCode, raw, decoded, path);
      final ok = resp.statusCode >= 200 && resp.statusCode < 300;
      return WorkerCommandResult(
        exitCode: ok ? 0 : resp.statusCode,
        output: output,
        json: decoded,
      );
    } on Object catch (error) {
      if (tryStart) {
        final start = await ensureRunning();
        if (start.ok) {
          return _request(method, path, body: body, tryStart: false);
        }
      }
      return WorkerCommandResult(
        exitCode: 1,
        output: 'Worker control API is not reachable at $_baseURL.\n\n$error',
      );
    } finally {
      client.close(force: true);
    }
  }

  Object? _decodeResponse(String raw) {
    if (raw.trim().isEmpty) return null;
    try {
      return jsonDecode(raw);
    } on Object {
      return null;
    }
  }

  String _formatResponse(
    int statusCode,
    String raw,
    Object? decoded,
    String path,
  ) {
    if (statusCode == HttpStatus.unauthorized && path.startsWith('/v1/')) {
      return 'Local worker control API rejected this app token.\n\n'
          'This usually means an older CMesh Worker control process is still running on 127.0.0.1:9781. '
          'Stop the old process once, then reopen the Worker App.';
    }
    if (raw.trim().isEmpty) {
      return 'HTTP $statusCode';
    }
    if (decoded != null) {
      return const JsonEncoder.withIndent('  ').convert(decoded);
    }
    return raw.trim();
  }

  Future<String?> _findControlBinary() async {
    final configured =
        Platform.environment['CMESH_WORKER_CONTROL_BIN'] ??
        Platform.environment['CMESH_BIN'];
    if (configured != null && configured.trim().isNotEmpty) {
      final file = File(configured.trim());
      if (await file.exists()) return file.path;
    }

    final executableName = Platform.isWindows ? 'cmesh.exe' : 'cmesh';
    final executable = File(Platform.resolvedExecutable);
    final executableDir = executable.parent;
    final candidates = <File>[
      File('${executableDir.path}/$executableName'),
      File('${executableDir.parent.path}/Resources/$executableName'),
      File('${Directory.current.path}/../../bin/$executableName'),
      File('${Directory.current.path}/bin/$executableName'),
      File('${Directory.current.parent.parent.path}/bin/$executableName'),
    ];
    for (final candidate in candidates) {
      if (await candidate.exists()) {
        return candidate.path;
      }
    }

    final lookup = await _lookupOnPath(executableName);
    return lookup;
  }

  Future<String?> _lookupOnPath(String executableName) async {
    final command = Platform.isWindows ? 'where' : 'which';
    try {
      final result = await Process.run(command, [executableName]);
      if (result.exitCode != 0) return null;
      final first = (result.stdout as String)
          .split(RegExp(r'\r?\n'))
          .map((line) => line.trim())
          .where((line) => line.isNotEmpty)
          .firstOrNull;
      return first;
    } on Object {
      return null;
    }
  }
}

String _generateControlToken() {
  final random = Random.secure();
  final bytes = List<int>.generate(32, (_) => random.nextInt(256));
  return base64UrlEncode(bytes);
}

class WorkerHomePage extends StatefulWidget {
  const WorkerHomePage({
    super.key,
    required this.initialInvite,
    required this.autostartControl,
    required this.registerProtocolHandler,
  });

  final InviteConfig? initialInvite;
  final bool autostartControl;
  final bool registerProtocolHandler;

  @override
  State<WorkerHomePage> createState() => _WorkerHomePageState();
}

class _WorkerHomePageState extends State<WorkerHomePage> {
  final _store = WorkerConfigStore();
  final _controller = WorkerController();
  final _protocolRegistrar = WorkerProtocolRegistrar();
  final _formKey = GlobalKey<FormState>();
  final _managerUrl = TextEditingController();
  final _joinToken = TextEditingController();
  final _cpu = TextEditingController();
  final _memoryGb = TextEditingController();
  final _diskGb = TextEditingController();
  final _vramGb = TextEditingController();

  bool _gpuEnabled = true;
  bool _benchmark = true;
  bool _installService = true;
  bool _busy = false;
  String _status = 'Idle';
  String _output = 'No command has been run yet.';
  WorkerRuntimeStatus? _runtimeStatus;

  @override
  void initState() {
    super.initState();
    PlatformInviteBridge.setInviteHandler(_applyInvite);
    _loadConfig();
    if (widget.registerProtocolHandler) {
      _registerProtocolHandler();
    }
    if (widget.autostartControl) {
      _bootstrapControlApi();
    }
  }

  @override
  void dispose() {
    _managerUrl.dispose();
    _joinToken.dispose();
    _cpu.dispose();
    _memoryGb.dispose();
    _diskGb.dispose();
    _vramGb.dispose();
    super.dispose();
  }

  Future<void> _loadConfig() async {
    final config = await _store.load();
    final platformInvite = await PlatformInviteBridge.initialInvite();
    if (!mounted) return;
    setState(() {
      final invite = platformInvite ?? widget.initialInvite;
      _managerUrl.text = invite?.managerUrl ?? config.managerUrl;
      _joinToken.text = invite?.joinToken ?? config.joinToken;
      _cpu.text = '${config.cpu}';
      _memoryGb.text = '${config.memoryGb}';
      _diskGb.text = '${config.diskGb}';
      _vramGb.text = '${config.vramGb}';
      _gpuEnabled = config.gpuEnabled;
      _benchmark = config.benchmark;
      _installService = config.installService;
    });
  }

  Future<void> _registerProtocolHandler() async {
    final result = await _protocolRegistrar.ensureRegistered();
    if (!mounted || result.ok) return;
    setState(() {
      _output = result.output;
    });
  }

  void _applyInvite(InviteConfig invite) {
    if (!mounted) return;
    setState(() {
      if (invite.managerUrl != null && invite.managerUrl!.isNotEmpty) {
        _managerUrl.text = invite.managerUrl!;
      }
      if (invite.joinToken != null && invite.joinToken!.isNotEmpty) {
        _joinToken.text = invite.joinToken!;
      }
      _status = 'Invite loaded';
    });
  }

  Future<void> _bootstrapControlApi() async {
    final result = await _controller.ensureRunning();
    if (!mounted) return;
    setState(() {
      _status = result.ok ? 'Control API ready' : 'Control API unavailable';
      _output = result.output;
    });
    if (result.ok) {
      await _refreshStatus();
    }
  }

  WorkerConfig _readConfig() {
    return WorkerConfig(
      managerUrl: _managerUrl.text.trim(),
      joinToken: _joinToken.text.trim(),
      cpu: int.parse(_cpu.text),
      memoryGb: int.parse(_memoryGb.text),
      diskGb: int.parse(_diskGb.text),
      gpuEnabled: _gpuEnabled,
      vramGb: int.parse(_vramGb.text),
      benchmark: _benchmark,
      installService: _installService,
    );
  }

  Future<void> _saveConfig() async {
    if (!_formKey.currentState!.validate()) return;
    await _run('Saving', () async {
      final config = _readConfig();
      await _store.save(config);
      return _controller.saveConfig(config);
    });
  }

  Future<void> _connect() async {
    if (!_formKey.currentState!.validate()) return;
    await _run('Connecting', () async {
      final config = _readConfig();
      await _store.save(config);
      return _controller.install(config);
    });
  }

  Future<void> _serviceAction(String action) {
    return _run(action, () => _controller.serviceAction(action));
  }

  Future<void> _refreshStatus() async {
    await _run('Refreshing status', () => _controller.serviceAction('status'));
  }

  Future<void> _run(
    String label,
    Future<WorkerCommandResult> Function() command,
  ) async {
    setState(() {
      _busy = true;
      _status = '$label...';
      _output = '';
    });
    final result = await command();
    if (!mounted) return;
    final runtimeStatus = _runtimeStatusFromResult(result);
    setState(() {
      _busy = false;
      if (runtimeStatus != null) {
        _runtimeStatus = runtimeStatus;
      }
      _status = result.ok
          ? runtimeStatus?.label ?? '$label complete'
          : '$label failed';
      _output = result.output.isEmpty
          ? 'Exit code ${result.exitCode}'
          : result.output;
    });
  }

  WorkerRuntimeStatus? _runtimeStatusFromResult(WorkerCommandResult result) {
    final json = result.json;
    if (json is! Map<String, dynamic>) return null;
    if (!json.containsKey('running')) return null;
    return WorkerRuntimeStatus.fromJson(json);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: SafeArea(
        child: Column(
          children: [
            _Header(status: _status, busy: _busy),
            Expanded(
              child: SingleChildScrollView(
                padding: const EdgeInsets.fromLTRB(24, 20, 24, 24),
                child: ConstrainedBox(
                  constraints: const BoxConstraints(maxWidth: 1180),
                  child: Form(
                    key: _formKey,
                    child: LayoutBuilder(
                      builder: (context, constraints) {
                        final wide = constraints.maxWidth >= 920;
                        final form = _ConnectionPanel(
                          managerUrl: _managerUrl,
                          joinToken: _joinToken,
                          cpu: _cpu,
                          memoryGb: _memoryGb,
                          diskGb: _diskGb,
                          vramGb: _vramGb,
                          gpuEnabled: _gpuEnabled,
                          benchmark: _benchmark,
                          installService: _installService,
                          onGpuChanged: (value) =>
                              setState(() => _gpuEnabled = value),
                          onBenchmarkChanged: (value) =>
                              setState(() => _benchmark = value),
                          onInstallServiceChanged: (value) =>
                              setState(() => _installService = value),
                        );
                        final controls = _ControlPanel(
                          busy: _busy,
                          output: _output,
                          runtimeStatus: _runtimeStatus,
                          onConnect: _connect,
                          onSave: _saveConfig,
                          onStatus: _refreshStatus,
                          onStart: () => _serviceAction('start'),
                          onStop: () => _serviceAction('stop'),
                          onDisconnect: () => _serviceAction('disconnect'),
                        );
                        if (!wide) {
                          return Column(
                            crossAxisAlignment: CrossAxisAlignment.stretch,
                            children: [
                              form,
                              const SizedBox(height: 16),
                              controls,
                            ],
                          );
                        }
                        return Row(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Expanded(flex: 5, child: form),
                            const SizedBox(width: 16),
                            Expanded(flex: 4, child: controls),
                          ],
                        );
                      },
                    ),
                  ),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _Header extends StatelessWidget {
  const _Header({required this.status, required this.busy});

  final String status;
  final bool busy;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 18),
      decoration: BoxDecoration(
        color: colors.surface,
        border: Border(bottom: BorderSide(color: colors.outlineVariant)),
      ),
      child: Row(
        children: [
          Container(
            width: 42,
            height: 42,
            decoration: BoxDecoration(
              color: colors.primaryContainer,
              borderRadius: BorderRadius.circular(8),
            ),
            child: Icon(Icons.hub_outlined, color: colors.onPrimaryContainer),
          ),
          const SizedBox(width: 14),
          const Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  'CMesh Worker',
                  style: TextStyle(fontSize: 20, fontWeight: FontWeight.w700),
                ),
                SizedBox(height: 2),
                Text(
                  'Join a private cluster and control local worker resources.',
                ),
              ],
            ),
          ),
          if (busy)
            const SizedBox.square(
              dimension: 18,
              child: CircularProgressIndicator(strokeWidth: 2),
            ),
          const SizedBox(width: 12),
          Chip(
            avatar: Icon(busy ? Icons.sync : Icons.circle, size: 14),
            label: Text(status),
          ),
        ],
      ),
    );
  }
}

class _ConnectionPanel extends StatelessWidget {
  const _ConnectionPanel({
    required this.managerUrl,
    required this.joinToken,
    required this.cpu,
    required this.memoryGb,
    required this.diskGb,
    required this.vramGb,
    required this.gpuEnabled,
    required this.benchmark,
    required this.installService,
    required this.onGpuChanged,
    required this.onBenchmarkChanged,
    required this.onInstallServiceChanged,
  });

  final TextEditingController managerUrl;
  final TextEditingController joinToken;
  final TextEditingController cpu;
  final TextEditingController memoryGb;
  final TextEditingController diskGb;
  final TextEditingController vramGb;
  final bool gpuEnabled;
  final bool benchmark;
  final bool installService;
  final ValueChanged<bool> onGpuChanged;
  final ValueChanged<bool> onBenchmarkChanged;
  final ValueChanged<bool> onInstallServiceChanged;

  @override
  Widget build(BuildContext context) {
    return _Panel(
      title: 'Connection',
      icon: Icons.settings_ethernet,
      child: Column(
        children: [
          TextFormField(
            controller: managerUrl,
            decoration: const InputDecoration(
              labelText: 'Manager URL',
              prefixIcon: Icon(Icons.public),
            ),
            validator: _requiredUrl,
          ),
          const SizedBox(height: 12),
          TextFormField(
            controller: joinToken,
            decoration: const InputDecoration(
              labelText: 'Join token',
              prefixIcon: Icon(Icons.key),
            ),
            obscureText: true,
            validator: _required,
          ),
          const SizedBox(height: 18),
          _SectionLabel('Resource limits'),
          const SizedBox(height: 10),
          LayoutBuilder(
            builder: (context, constraints) {
              final columns = constraints.maxWidth > 560 ? 3 : 1;
              return _FieldGrid(
                columns: columns,
                children: [
                  _NumberField(
                    controller: cpu,
                    label: 'CPU cores',
                    icon: Icons.memory,
                  ),
                  _NumberField(
                    controller: memoryGb,
                    label: 'RAM GB',
                    icon: Icons.storage,
                  ),
                  _NumberField(
                    controller: diskGb,
                    label: 'Disk GB',
                    icon: Icons.folder,
                  ),
                  _NumberField(
                    controller: vramGb,
                    label: 'VRAM GB',
                    icon: Icons.view_in_ar,
                    allowZero: true,
                  ),
                ],
              );
            },
          ),
          const SizedBox(height: 8),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Allow GPU usage'),
            value: gpuEnabled,
            onChanged: onGpuChanged,
          ),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Run benchmark after connect'),
            value: benchmark,
            onChanged: onBenchmarkChanged,
          ),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Run in background and start on login/boot'),
            value: installService,
            onChanged: onInstallServiceChanged,
          ),
        ],
      ),
    );
  }
}

class _ControlPanel extends StatelessWidget {
  const _ControlPanel({
    required this.busy,
    required this.output,
    required this.runtimeStatus,
    required this.onConnect,
    required this.onSave,
    required this.onStatus,
    required this.onStart,
    required this.onStop,
    required this.onDisconnect,
  });

  final bool busy;
  final String output;
  final WorkerRuntimeStatus? runtimeStatus;
  final VoidCallback onConnect;
  final VoidCallback onSave;
  final VoidCallback onStatus;
  final VoidCallback onStart;
  final VoidCallback onStop;
  final VoidCallback onDisconnect;

  @override
  Widget build(BuildContext context) {
    return _Panel(
      title: 'Worker control',
      icon: Icons.power_settings_new,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          _RuntimeStatusCard(status: runtimeStatus),
          const SizedBox(height: 14),
          FilledButton.icon(
            onPressed: busy ? null : onConnect,
            icon: const Icon(Icons.link),
            label: const Text('Connect worker'),
          ),
          const SizedBox(height: 10),
          OutlinedButton.icon(
            onPressed: busy ? null : onSave,
            icon: const Icon(Icons.save_outlined),
            label: const Text('Save config'),
          ),
          const SizedBox(height: 14),
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              _ActionButton(
                icon: Icons.fact_check_outlined,
                label: 'Status',
                onPressed: busy ? null : onStatus,
              ),
              _ActionButton(
                icon: Icons.play_arrow,
                label: 'Start',
                onPressed: busy ? null : onStart,
              ),
              _ActionButton(
                icon: Icons.stop,
                label: 'Stop',
                onPressed: busy ? null : onStop,
              ),
              _ActionButton(
                icon: Icons.link_off,
                label: 'Disconnect',
                onPressed: busy ? null : onDisconnect,
              ),
            ],
          ),
          const SizedBox(height: 18),
          _SectionLabel('Output'),
          const SizedBox(height: 8),
          Container(
            constraints: const BoxConstraints(minHeight: 260),
            padding: const EdgeInsets.all(12),
            decoration: BoxDecoration(
              color: const Color(0xFF101418),
              borderRadius: BorderRadius.circular(8),
            ),
            child: SelectableText(
              output,
              style: const TextStyle(
                color: Color(0xFFE6EDF3),
                fontFamily: 'monospace',
                fontSize: 12,
                height: 1.35,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class _RuntimeStatusCard extends StatelessWidget {
  const _RuntimeStatusCard({required this.status});

  final WorkerRuntimeStatus? status;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    final current = status;
    final running = current?.running ?? false;
    final color = running
        ? const Color(0xFF1B7F4B)
        : current?.lastError?.isNotEmpty == true
        ? colors.error
        : colors.outline;
    return Container(
      padding: const EdgeInsets.all(14),
      decoration: BoxDecoration(
        color: colors.surfaceContainerHighest,
        borderRadius: BorderRadius.circular(8),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                running ? Icons.check_circle : Icons.pause_circle,
                color: color,
                size: 20,
              ),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  current?.label ?? 'Status unknown',
                  style: Theme.of(
                    context,
                  ).textTheme.titleSmall?.copyWith(fontWeight: FontWeight.w700),
                ),
              ),
            ],
          ),
          const SizedBox(height: 10),
          _StatusLine(
            label: 'PID',
            value: current?.pid == null ? '-' : '${current!.pid}',
          ),
          _StatusLine(
            label: 'Started',
            value: _formatStartedAt(current?.startedAt),
          ),
          _StatusLine(
            label: 'Exit code',
            value: current?.exitCode == null ? '-' : '${current!.exitCode}',
          ),
          if (current?.lastError?.isNotEmpty == true) ...[
            const SizedBox(height: 8),
            Text(
              current!.lastError!,
              style: TextStyle(color: colors.error, fontSize: 12),
            ),
          ],
        ],
      ),
    );
  }

  String _formatStartedAt(DateTime? value) {
    if (value == null) return '-';
    final local = value.toLocal();
    return '${local.year.toString().padLeft(4, '0')}-'
        '${local.month.toString().padLeft(2, '0')}-'
        '${local.day.toString().padLeft(2, '0')} '
        '${local.hour.toString().padLeft(2, '0')}:'
        '${local.minute.toString().padLeft(2, '0')}:'
        '${local.second.toString().padLeft(2, '0')}';
  }
}

class _StatusLine extends StatelessWidget {
  const _StatusLine({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(top: 4),
      child: Row(
        children: [
          SizedBox(
            width: 78,
            child: Text(
              label,
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: Theme.of(context).colorScheme.onSurfaceVariant,
              ),
            ),
          ),
          Expanded(
            child: Text(
              value,
              style: Theme.of(
                context,
              ).textTheme.bodySmall?.copyWith(fontWeight: FontWeight.w600),
            ),
          ),
        ],
      ),
    );
  }
}

class _Panel extends StatelessWidget {
  const _Panel({required this.title, required this.icon, required this.child});

  final String title;
  final IconData icon;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.all(18),
      decoration: BoxDecoration(
        border: Border.all(color: colors.outlineVariant),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(icon, color: colors.primary),
              const SizedBox(width: 8),
              Text(
                title,
                style: Theme.of(
                  context,
                ).textTheme.titleMedium?.copyWith(fontWeight: FontWeight.w700),
              ),
            ],
          ),
          const SizedBox(height: 16),
          child,
        ],
      ),
    );
  }
}

class _SectionLabel extends StatelessWidget {
  const _SectionLabel(this.label);

  final String label;

  @override
  Widget build(BuildContext context) {
    return Align(
      alignment: Alignment.centerLeft,
      child: Text(
        label,
        style: Theme.of(
          context,
        ).textTheme.labelLarge?.copyWith(fontWeight: FontWeight.w700),
      ),
    );
  }
}

class _FieldGrid extends StatelessWidget {
  const _FieldGrid({required this.columns, required this.children});

  final int columns;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    return Wrap(
      spacing: 10,
      runSpacing: 10,
      children: children
          .map(
            (child) => SizedBox(
              width: columns == 1
                  ? double.infinity
                  : ((MediaQuery.sizeOf(context).width - 120) / columns).clamp(
                      140,
                      220,
                    ),
              child: child,
            ),
          )
          .toList(),
    );
  }
}

class _NumberField extends StatelessWidget {
  const _NumberField({
    required this.controller,
    required this.label,
    required this.icon,
    this.allowZero = false,
  });

  final TextEditingController controller;
  final String label;
  final IconData icon;
  final bool allowZero;

  @override
  Widget build(BuildContext context) {
    return TextFormField(
      controller: controller,
      decoration: InputDecoration(labelText: label, prefixIcon: Icon(icon)),
      keyboardType: TextInputType.number,
      inputFormatters: [FilteringTextInputFormatter.digitsOnly],
      validator: allowZero ? _nonNegativeInt : _positiveInt,
    );
  }
}

class _ActionButton extends StatelessWidget {
  const _ActionButton({
    required this.icon,
    required this.label,
    required this.onPressed,
  });

  final IconData icon;
  final String label;
  final VoidCallback? onPressed;

  @override
  Widget build(BuildContext context) {
    return OutlinedButton.icon(
      onPressed: onPressed,
      icon: Icon(icon, size: 18),
      label: Text(label),
    );
  }
}

String? _required(String? value) {
  if (value == null || value.trim().isEmpty) {
    return 'Required';
  }
  return null;
}

String? _requiredUrl(String? value) {
  final required = _required(value);
  if (required != null) return required;
  final uri = Uri.tryParse(value!.trim());
  if (uri == null || !uri.hasScheme || uri.host.isEmpty) {
    return 'Use a full URL';
  }
  return null;
}

String? _positiveInt(String? value) {
  if (value == null || value.isEmpty) {
    return 'Required';
  }
  final parsed = int.tryParse(value);
  if (parsed == null || parsed <= 0) {
    return 'Use 1 or more';
  }
  return null;
}

String? _nonNegativeInt(String? value) {
  if (value == null || value.isEmpty) {
    return 'Required';
  }
  final parsed = int.tryParse(value);
  if (parsed == null || parsed < 0) {
    return 'Invalid';
  }
  return null;
}
