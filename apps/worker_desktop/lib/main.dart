import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

void main() {
  runApp(const CMeshWorkerApp());
}

class CMeshWorkerApp extends StatelessWidget {
  const CMeshWorkerApp({super.key});

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
      home: const WorkerHomePage(),
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
      managerUrl: json['managerUrl'] as String? ?? '',
      joinToken: json['joinToken'] as String? ?? '',
      cpu: json['cpu'] as int? ?? Platform.numberOfProcessors,
      memoryGb: json['memoryGb'] as int? ?? 8,
      diskGb: json['diskGb'] as int? ?? 50,
      gpuEnabled: json['gpuEnabled'] as bool? ?? true,
      vramGb: json['vramGb'] as int? ?? 0,
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
  const WorkerCommandResult({required this.exitCode, required this.output});

  final int exitCode;
  final String output;

  bool get ok => exitCode == 0;
}

class WorkerController {
  static const installerUrl =
      'https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh';
  static const windowsInstallerUrl =
      'https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.ps1';

  Future<WorkerCommandResult> install(WorkerConfig config) {
    if (Platform.isWindows) {
      return _runWindowsInstall(config);
    }
    return _runUnixInstall(config);
  }

  Future<WorkerCommandResult> serviceAction(String action) {
    if (Platform.isWindows) {
      return _runWindowsAction(action);
    }
    return _runUnixAction(action);
  }

  Future<WorkerCommandResult> _runUnixInstall(WorkerConfig config) {
    final env = _environment(config);
    final script = 'curl -fsSL $installerUrl | sh';
    return _run('/bin/sh', ['-lc', script], env);
  }

  Future<WorkerCommandResult> _runUnixAction(String action) {
    final script = 'curl -fsSL $installerUrl | sh -s -- $action';
    return _run('/bin/sh', ['-lc', script], const {});
  }

  Future<WorkerCommandResult> _runWindowsInstall(WorkerConfig config) {
    final env = _environment(config);
    final script = 'iwr $windowsInstallerUrl -UseB | iex';
    return _run('powershell.exe', [
      '-NoProfile',
      '-ExecutionPolicy',
      'Bypass',
      '-Command',
      script,
    ], env);
  }

  Future<WorkerCommandResult> _runWindowsAction(String action) {
    final script =
        r'$script = (iwr '
        '$windowsInstallerUrl'
        r' -UseB).Content; '
        'iex "& { \$script } -Action $action"';
    return _run('powershell.exe', [
      '-NoProfile',
      '-ExecutionPolicy',
      'Bypass',
      '-Command',
      script,
    ], const {});
  }

  Future<WorkerCommandResult> _run(
    String executable,
    List<String> arguments,
    Map<String, String> extraEnv,
  ) async {
    try {
      final result = await Process.run(
        executable,
        arguments,
        environment: {...Platform.environment, ...extraEnv},
      );
      final output = [
        if ((result.stdout as String).trim().isNotEmpty)
          result.stdout as String,
        if ((result.stderr as String).trim().isNotEmpty)
          result.stderr as String,
      ].join('\n').trim();
      return WorkerCommandResult(exitCode: result.exitCode, output: output);
    } on Object catch (error) {
      return WorkerCommandResult(exitCode: 1, output: error.toString());
    }
  }

  Map<String, String> _environment(WorkerConfig config) {
    return {
      'CMESH_MANAGER_URL': config.managerUrl,
      'CMESH_JOIN_TOKEN': config.joinToken,
      'CMESH_CPU': '${config.cpu}',
      'CMESH_MEMORY_GB': '${config.memoryGb}',
      'CMESH_DISK_GB': '${config.diskGb}',
      'CMESH_GPU': '${config.gpuEnabled}',
      'CMESH_VRAM_GB': '${config.vramGb}',
      'CMESH_BENCHMARK': '${config.benchmark}',
      'CMESH_INSTALL_SERVICE': '${config.installService}',
    };
  }
}

class WorkerHomePage extends StatefulWidget {
  const WorkerHomePage({super.key});

  @override
  State<WorkerHomePage> createState() => _WorkerHomePageState();
}

class _WorkerHomePageState extends State<WorkerHomePage> {
  final _store = WorkerConfigStore();
  final _controller = WorkerController();
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

  @override
  void initState() {
    super.initState();
    _loadConfig();
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
    if (!mounted) return;
    setState(() {
      _managerUrl.text = config.managerUrl;
      _joinToken.text = config.joinToken;
      _cpu.text = '${config.cpu}';
      _memoryGb.text = '${config.memoryGb}';
      _diskGb.text = '${config.diskGb}';
      _vramGb.text = '${config.vramGb}';
      _gpuEnabled = config.gpuEnabled;
      _benchmark = config.benchmark;
      _installService = config.installService;
    });
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
    await _store.save(_readConfig());
    _setOutput('Saved', 'Config saved to ~/.cmesh/worker-desktop.json');
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
    setState(() {
      _busy = false;
      _status = result.ok ? '$label complete' : '$label failed';
      _output = result.output.isEmpty
          ? 'Exit code ${result.exitCode}'
          : result.output;
    });
  }

  void _setOutput(String status, String output) {
    setState(() {
      _status = status;
      _output = output;
    });
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
                          onConnect: _connect,
                          onSave: _saveConfig,
                          onStatus: () => _serviceAction('status'),
                          onStart: () => _serviceAction('start'),
                          onStop: () => _serviceAction('stop'),
                          onUninstall: () => _serviceAction('uninstall'),
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
    required this.onConnect,
    required this.onSave,
    required this.onStatus,
    required this.onStart,
    required this.onStop,
    required this.onUninstall,
  });

  final bool busy;
  final String output;
  final VoidCallback onConnect;
  final VoidCallback onSave;
  final VoidCallback onStatus;
  final VoidCallback onStart;
  final VoidCallback onStop;
  final VoidCallback onUninstall;

  @override
  Widget build(BuildContext context) {
    return _Panel(
      title: 'Worker control',
      icon: Icons.power_settings_new,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
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
                icon: Icons.delete_outline,
                label: 'Uninstall',
                onPressed: busy ? null : onUninstall,
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
  });

  final TextEditingController controller;
  final String label;
  final IconData icon;

  @override
  Widget build(BuildContext context) {
    return TextFormField(
      controller: controller,
      decoration: InputDecoration(labelText: label, prefixIcon: Icon(icon)),
      keyboardType: TextInputType.number,
      inputFormatters: [FilteringTextInputFormatter.digitsOnly],
      validator: _positiveInt,
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
  if (parsed == null || parsed < 0) {
    return 'Invalid';
  }
  return null;
}
