#include "gguf.h"
#include "ggml.h"
#include "llama.h"

#include <algorithm>
#include <fstream>
#include <cstdint>
#include <cstdlib>
#include <cctype>
#include <cstring>
#include <filesystem>
#include <map>
#include <sstream>
#include <iostream>
#include <limits>
#include <numeric>
#include <string>
#include <vector>

struct TensorManifestEntry {
    std::string name;
    std::string type;
    uint64_t bytes = 0;
    bool boundary = false;
};

struct TensorManifest {
    int64_t total_tensor_count = 0;
    int64_t stage_tensor_count = 0;
    int64_t boundary_tensor_count = 0;
    uint64_t selected_bytes = 0;
    std::vector<TensorManifestEntry> selected;
};

struct MaterializationProbe {
    bool requested = false;
    bool attempted = false;
    bool loaded = false;
    std::string status = "not_requested";
    std::string error;
};

struct ShardBundleTensor {
    std::string name;
    std::string type;
    uint64_t source_offset = 0;
    uint64_t payload_offset = 0;
    uint64_t bytes = 0;
    bool boundary = false;
};

struct ShardBundleHeader {
    std::string json;
    uint64_t header_bytes = 0;
    uint64_t payload_offset = 0;
    uint64_t payload_bytes = 0;
    uint64_t bundle_bytes = 0;
    uint64_t stage_index = 0;
    uint64_t stage_start = 0;
    uint64_t stage_end = 0;
    uint64_t selected_tensor_count = 0;
    uint64_t selected_bytes = 0;
    bool loadable_gguf = false;
    std::vector<ShardBundleTensor> tensors;
};

struct LoadedShardTensor {
    ShardBundleTensor meta;
    std::vector<uint8_t> data;
};

struct StageGGUFLoadPlan {
    bool stage_metadata = false;
    int32_t stage_start = -1;
    int32_t stage_end = -1;
    uint64_t selected_tensor_count = 0;
    std::vector<std::string> tensor_names;
    std::vector<const char *> tensor_allowlist;
};

struct ShapeSpec {
    std::vector<int> dims;
    int tokens = 0;
    int n_embd = 0;
};

struct ResidentLoopSession {
    std::string session_id;
    std::string model_path;
    int stage_index = -1;
    int stage_start = -1;
    int stage_end = -1;
    int decode_steps = 0;
    int last_step = 0;
    std::string last_stage_command;
    llama_model * model = nullptr;
    llama_context * ctx = nullptr;
    int n_layer = 0;
    int n_embd = 0;
    uint64_t selected_bytes = 0;
    uint64_t selected_tensor_count = 0;
    bool native_prepared = false;
    std::vector<llama_token> tokens;
};

static std::string json_escape(const std::string & value) {
    std::string out;
    out.reserve(value.size() + 8);
    for (char ch : value) {
        switch (ch) {
            case '\\': out += "\\\\"; break;
            case '"':  out += "\\\""; break;
            case '\n': out += "\\n";  break;
            case '\r': out += "\\r";  break;
            case '\t': out += "\\t";  break;
            default:   out += ch;      break;
        }
    }
    return out;
}

static bool load_stage_gguf_plan(const std::string & model_path, StageGGUFLoadPlan & plan, std::string & error) {
    plan = StageGGUFLoadPlan{};

    gguf_init_params gguf_params = {};
    gguf_params.no_alloc = true;
    gguf_context * gguf = gguf_init_from_file(model_path.c_str(), gguf_params);
    if (!gguf) {
        error = "failed to open GGUF metadata: " + model_path;
        return false;
    }

    const int layer_start_key = gguf_find_key(gguf, "cmesh.shard.layer_start");
    const int layer_end_key = gguf_find_key(gguf, "cmesh.shard.layer_end");
    const int selected_count_key = gguf_find_key(gguf, "cmesh.shard.selected_tensor_count");
    plan.stage_metadata = layer_start_key >= 0 && layer_end_key >= 0;
    if (plan.stage_metadata) {
        plan.stage_start = static_cast<int32_t>(gguf_get_val_u64(gguf, layer_start_key));
        plan.stage_end = static_cast<int32_t>(gguf_get_val_u64(gguf, layer_end_key));
        plan.selected_tensor_count = selected_count_key >= 0 ? gguf_get_val_u64(gguf, selected_count_key) : 0;

        const int64_t n_tensors = gguf_get_n_tensors(gguf);
        plan.tensor_names.reserve(n_tensors > 0 ? static_cast<size_t>(n_tensors) : 0);
        plan.tensor_allowlist.reserve(n_tensors > 0 ? static_cast<size_t>(n_tensors) : 0);
        for (int64_t i = 0; i < n_tensors; ++i) {
            const char * name = gguf_get_tensor_name(gguf, i);
            if (name != nullptr && name[0] != '\0') {
                plan.tensor_names.emplace_back(name);
            }
        }
        for (const auto & name : plan.tensor_names) {
            plan.tensor_allowlist.push_back(name.c_str());
        }
    }

    gguf_free(gguf);
    return true;
}

static void apply_stage_gguf_plan(llama_model_params & params, const StageGGUFLoadPlan & plan) {
    if (!plan.stage_metadata || plan.tensor_allowlist.empty()) {
        return;
    }
    params.cmesh_stage_layer_start = plan.stage_start;
    params.cmesh_stage_layer_end = plan.stage_end;
    params.cmesh_stage_tensor_allowlist = plan.tensor_allowlist.data();
    params.cmesh_stage_tensor_allowlist_count = plan.tensor_allowlist.size();
    params.cmesh_stage_partial_load = true;
}

static bool has_arg(const std::vector<std::string> & args, const std::string & name) {
    for (const auto & arg : args) {
        if (arg == name) {
            return true;
        }
    }
    return false;
}

static std::map<std::string, std::string> parse_kv_line(const std::string & line) {
    std::map<std::string, std::string> values;
    std::istringstream in(line);
    std::string part;
    while (in >> part) {
        const size_t eq = part.find('=');
        if (eq == std::string::npos || eq == 0) {
            continue;
        }
        values[part.substr(0, eq)] = part.substr(eq + 1);
    }
    return values;
}

static int kv_int_value(const std::map<std::string, std::string> & values, const std::string & name, int fallback) {
    auto it = values.find(name);
    if (it == values.end() || it->second.empty()) {
        return fallback;
    }
    char * end = nullptr;
    const long parsed = std::strtol(it->second.c_str(), &end, 10);
    if (end == it->second.c_str() || *end != '\0') {
        return fallback;
    }
    return static_cast<int>(parsed);
}

static std::string kv_string_value(const std::map<std::string, std::string> & values, const std::string & name) {
    auto it = values.find(name);
    if (it == values.end()) {
        return "";
    }
    return it->second;
}

static bool env_bool_value(const char * name, bool fallback) {
    const char * raw = std::getenv(name);
    if (!raw || raw[0] == '\0') {
        return fallback;
    }
    std::string value(raw);
    std::transform(value.begin(), value.end(), value.begin(), [](unsigned char ch) {
        return static_cast<char>(std::tolower(ch));
    });
    if (value == "0" || value == "false" || value == "no" || value == "off") {
        return false;
    }
    if (value == "1" || value == "true" || value == "yes" || value == "on") {
        return true;
    }
    return fallback;
}

static std::string env_string_value(const char * name) {
    const char * raw = std::getenv(name);
    if (!raw) {
        return "";
    }
    return std::string(raw);
}

static std::string stage_session_file() {
    return env_string_value("CMESH_STAGE_SESSION_FILE");
}

static bool path_exists(const std::string & path) {
    if (path.empty()) {
        return false;
    }
    std::error_code ec;
    return std::filesystem::exists(path, ec);
}

static uint32_t stage_context_size(int requested, int minimum_tokens) {
    if (requested > 0) {
        return static_cast<uint32_t>(requested);
    }
    return static_cast<uint32_t>(std::max(minimum_tokens, 2048));
}

static size_t load_stage_sequence_state(llama_context * ctx, const std::string & path, std::vector<llama_token> & tokens, std::string & error) {
    if (path.empty() || !path_exists(path)) {
        return 0;
    }
    const uint32_t n_ctx = llama_n_ctx(ctx);
    tokens.assign(static_cast<size_t>(std::max<uint32_t>(n_ctx, 1)), 0);
    size_t token_count = 0;
    const size_t bytes = llama_state_seq_load_file(ctx, path.c_str(), 0, tokens.data(), tokens.size(), &token_count);
    if (bytes == 0) {
        tokens.clear();
        error = "failed to load stage sequence state: " + path;
        return 0;
    }
    tokens.resize(token_count);
    return bytes;
}

static size_t save_stage_sequence_state(llama_context * ctx, const std::string & path, const std::vector<llama_token> & tokens, std::string & error) {
    if (path.empty()) {
        return 0;
    }
    std::error_code ec;
    std::filesystem::create_directories(std::filesystem::path(path).parent_path(), ec);
    if (ec) {
        error = "failed to create stage sequence state directory: " + ec.message();
        return 0;
    }
    const size_t bytes = llama_state_seq_save_file(ctx, path.c_str(), 0, tokens.data(), tokens.size());
    if (bytes == 0) {
        error = "failed to save stage sequence state: " + path;
        return 0;
    }
    return bytes;
}

static int next_sequence_position(llama_context * ctx, const std::vector<llama_token> & fallback_tokens) {
    llama_memory_t memory = llama_get_memory(ctx);
    if (memory) {
        const llama_pos max_pos = llama_memory_seq_pos_max(memory, 0);
        if (max_pos >= 0 && max_pos < std::numeric_limits<int>::max()) {
            return static_cast<int>(max_pos) + 1;
        }
    }
    return static_cast<int>(fallback_tokens.size());
}

static void resize_with_placeholder_tokens(std::vector<llama_token> & tokens, size_t size) {
    if (tokens.size() < size) {
        tokens.resize(size, 0);
    }
}

static std::string arg_value(const std::vector<std::string> & args, const std::string & name, const std::string & fallback = "") {
    for (size_t i = 0; i + 1 < args.size(); ++i) {
        if (args[i] == name) {
            return args[i + 1];
        }
    }
    return fallback;
}

static int arg_int_value(const std::vector<std::string> & args, const std::string & name, int fallback = -1) {
    const std::string value = arg_value(args, name);
    if (value.empty()) {
        return fallback;
    }
    char * end = nullptr;
    const long parsed = std::strtol(value.c_str(), &end, 10);
    if (end == value.c_str() || *end != '\0') {
        return fallback;
    }
    return static_cast<int>(parsed);
}

static size_t arg_size_value(const std::vector<std::string> & args, const std::string & name, size_t fallback) {
    const std::string value = arg_value(args, name);
    if (value.empty()) {
        return fallback;
    }
    char * end = nullptr;
    const unsigned long parsed = std::strtoul(value.c_str(), &end, 10);
    if (end == value.c_str() || *end != '\0') {
        return fallback;
    }
    return static_cast<size_t>(parsed);
}

static bool read_file(const std::string & path, std::vector<uint8_t> & out, std::string & error) {
    std::ifstream file(path, std::ios::binary);
    if (!file) {
        error = "failed to open file: " + path;
        return false;
    }
    file.seekg(0, std::ios::end);
    const std::streamoff size = file.tellg();
    if (size < 0) {
        error = "failed to size file: " + path;
        return false;
    }
    file.seekg(0, std::ios::beg);
    out.resize(static_cast<size_t>(size));
    if (!out.empty()) {
        file.read(reinterpret_cast<char *>(out.data()), static_cast<std::streamsize>(out.size()));
        if (!file) {
            error = "failed to read file: " + path;
            return false;
        }
    }
    return true;
}

static bool write_file(const std::string & path, const uint8_t * data, size_t size, std::string & error) {
    std::ofstream file(path, std::ios::binary | std::ios::trunc);
    if (!file) {
        error = "failed to open output file: " + path;
        return false;
    }
    if (size > 0) {
        file.write(reinterpret_cast<const char *>(data), static_cast<std::streamsize>(size));
    }
    if (!file) {
        error = "failed to write output file: " + path;
        return false;
    }
    return true;
}

static bool copy_stream_range(std::ifstream & in, std::ofstream & out, uint64_t offset, uint64_t bytes, std::string & error) {
    in.clear();
    in.seekg(static_cast<std::streamoff>(offset), std::ios::beg);
    if (!in) {
        error = "failed to seek source model";
        return false;
    }
    std::vector<char> buffer(1024 * 1024);
    uint64_t remaining = bytes;
    while (remaining > 0) {
        const size_t chunk = static_cast<size_t>(std::min<uint64_t>(remaining, buffer.size()));
        in.read(buffer.data(), static_cast<std::streamsize>(chunk));
        if (!in) {
            error = "failed to read source tensor bytes";
            return false;
        }
        out.write(buffer.data(), static_cast<std::streamsize>(chunk));
        if (!out) {
            error = "failed to write shard tensor bytes";
            return false;
        }
        remaining -= chunk;
    }
    return true;
}

static bool compare_stream_ranges(
    std::ifstream & left,
    uint64_t left_offset,
    std::ifstream & right,
    uint64_t right_offset,
    uint64_t bytes,
    uint64_t & compared_bytes,
    std::string & error
) {
    left.clear();
    right.clear();
    left.seekg(static_cast<std::streamoff>(left_offset), std::ios::beg);
    right.seekg(static_cast<std::streamoff>(right_offset), std::ios::beg);
    if (!left || !right) {
        error = "failed to seek tensor ranges";
        return false;
    }

    std::vector<char> left_buffer(1024 * 1024);
    std::vector<char> right_buffer(left_buffer.size());
    uint64_t remaining = bytes;
    compared_bytes = 0;
    while (remaining > 0) {
        const size_t chunk = static_cast<size_t>(std::min<uint64_t>(remaining, left_buffer.size()));
        left.read(left_buffer.data(), static_cast<std::streamsize>(chunk));
        right.read(right_buffer.data(), static_cast<std::streamsize>(chunk));
        if (!left || !right) {
            error = "failed to read tensor ranges";
            return false;
        }
        if (std::memcmp(left_buffer.data(), right_buffer.data(), chunk) != 0) {
            error = "tensor payload mismatch";
            return false;
        }
        remaining -= chunk;
        compared_bytes += chunk;
    }
    return true;
}

static void write_u64_le(std::ofstream & out, uint64_t value) {
    unsigned char bytes[8];
    for (int i = 0; i < 8; ++i) {
        bytes[i] = static_cast<unsigned char>((value >> (i * 8)) & 0xff);
    }
    out.write(reinterpret_cast<const char *>(bytes), sizeof(bytes));
}

static bool read_u64_le(std::ifstream & in, uint64_t & value, std::string & error) {
    unsigned char bytes[8];
    in.read(reinterpret_cast<char *>(bytes), sizeof(bytes));
    if (!in) {
        error = "failed to read uint64 field";
        return false;
    }
    value = 0;
    for (int i = 0; i < 8; ++i) {
        value |= static_cast<uint64_t>(bytes[i]) << (i * 8);
    }
    return true;
}

static bool json_find_number_u64(const std::string & json, const std::string & key, uint64_t & value) {
    const std::string needle = "\"" + key + "\":";
    const size_t at = json.find(needle);
    if (at == std::string::npos) {
        return false;
    }
    size_t pos = at + needle.size();
    while (pos < json.size() && std::isspace(static_cast<unsigned char>(json[pos]))) {
        ++pos;
    }
    size_t end = pos;
    while (end < json.size() && std::isdigit(static_cast<unsigned char>(json[end]))) {
        ++end;
    }
    if (end == pos) {
        return false;
    }
    char * parsed_end = nullptr;
    const unsigned long long parsed = std::strtoull(json.substr(pos, end - pos).c_str(), &parsed_end, 10);
    if (!parsed_end || *parsed_end != '\0') {
        return false;
    }
    value = static_cast<uint64_t>(parsed);
    return true;
}

static bool json_find_bool(const std::string & json, const std::string & key, bool & value) {
    const std::string needle = "\"" + key + "\":";
    const size_t at = json.find(needle);
    if (at == std::string::npos) {
        return false;
    }
    size_t pos = at + needle.size();
    while (pos < json.size() && std::isspace(static_cast<unsigned char>(json[pos]))) {
        ++pos;
    }
    if (json.compare(pos, 4, "true") == 0) {
        value = true;
        return true;
    }
    if (json.compare(pos, 5, "false") == 0) {
        value = false;
        return true;
    }
    return false;
}

static bool json_find_string_in_object(const std::string & object, const std::string & key, std::string & value) {
    const std::string needle = "\"" + key + "\":\"";
    const size_t at = object.find(needle);
    if (at == std::string::npos) {
        return false;
    }
    size_t pos = at + needle.size();
    std::string out;
    while (pos < object.size()) {
        const char ch = object[pos++];
        if (ch == '"') {
            value = out;
            return true;
        }
        if (ch == '\\' && pos < object.size()) {
            const char escaped = object[pos++];
            switch (escaped) {
                case 'n': out += '\n'; break;
                case 'r': out += '\r'; break;
                case 't': out += '\t'; break;
                case '\\': out += '\\'; break;
                case '"': out += '"'; break;
                default: out += escaped; break;
            }
            continue;
        }
        out += ch;
    }
    return false;
}

static bool parse_shard_bundle_tensor_objects(const std::string & header_json, std::vector<ShardBundleTensor> & tensors, std::string & error) {
    const std::string needle = "\"tensors\":[";
    const size_t array_at = header_json.find(needle);
    if (array_at == std::string::npos) {
        error = "shard bundle header is missing tensors array";
        return false;
    }
    size_t pos = array_at + needle.size();
    while (pos < header_json.size()) {
        while (pos < header_json.size() && (std::isspace(static_cast<unsigned char>(header_json[pos])) || header_json[pos] == ',')) {
            ++pos;
        }
        if (pos >= header_json.size()) {
            break;
        }
        if (header_json[pos] == ']') {
            return true;
        }
        if (header_json[pos] != '{') {
            error = "invalid tensor object in shard bundle header";
            return false;
        }
        const size_t object_start = pos;
        int depth = 0;
        bool in_string = false;
        bool escaped = false;
        for (; pos < header_json.size(); ++pos) {
            const char ch = header_json[pos];
            if (in_string) {
                if (escaped) {
                    escaped = false;
                } else if (ch == '\\') {
                    escaped = true;
                } else if (ch == '"') {
                    in_string = false;
                }
                continue;
            }
            if (ch == '"') {
                in_string = true;
            } else if (ch == '{') {
                ++depth;
            } else if (ch == '}') {
                --depth;
                if (depth == 0) {
                    ++pos;
                    break;
                }
            }
        }
        if (depth != 0 || pos > header_json.size()) {
            error = "unterminated tensor object in shard bundle header";
            return false;
        }
        const std::string object = header_json.substr(object_start, pos - object_start);
        ShardBundleTensor tensor;
        uint64_t boundary_raw = 0;
        if (!json_find_string_in_object(object, "name", tensor.name) ||
            !json_find_string_in_object(object, "type", tensor.type) ||
            !json_find_number_u64(object, "source_offset", tensor.source_offset) ||
            !json_find_number_u64(object, "payload_offset", tensor.payload_offset) ||
            !json_find_number_u64(object, "bytes", tensor.bytes)) {
            error = "tensor object is missing required fields";
            return false;
        }
        if (json_find_bool(object, "boundary", tensor.boundary)) {
            // parsed
        } else if (json_find_number_u64(object, "boundary", boundary_raw)) {
            tensor.boundary = boundary_raw != 0;
        } else {
            error = "tensor object is missing boundary field";
            return false;
        }
        tensors.push_back(tensor);
    }
    error = "unterminated tensors array in shard bundle header";
    return false;
}

static bool read_shard_bundle_header(const std::string & bundle_path, ShardBundleHeader & header, std::string & error) {
    std::ifstream in(bundle_path, std::ios::binary);
    if (!in) {
        error = "failed to open shard bundle: " + bundle_path;
        return false;
    }

    const std::string magic = "CMESH_SHARD_BUNDLE_V1\n";
    std::vector<char> magic_buf(magic.size());
    in.read(magic_buf.data(), static_cast<std::streamsize>(magic_buf.size()));
    if (!in || std::string(magic_buf.data(), magic_buf.size()) != magic) {
        error = "invalid shard bundle magic header";
        return false;
    }

    if (!read_u64_le(in, header.header_bytes, error)) {
        return false;
    }
    if (header.header_bytes == 0 || header.header_bytes > 128 * 1024 * 1024) {
        error = "invalid shard bundle header size";
        return false;
    }

    header.json.resize(static_cast<size_t>(header.header_bytes));
    in.read(header.json.data(), static_cast<std::streamsize>(header.json.size()));
    if (!in) {
        error = "failed to read shard bundle header";
        return false;
    }

    std::error_code ec;
    header.bundle_bytes = static_cast<uint64_t>(std::filesystem::file_size(bundle_path, ec));
    if (ec) {
        error = "failed to stat shard bundle: " + ec.message();
        return false;
    }
    header.payload_offset = static_cast<uint64_t>(magic.size()) + sizeof(uint64_t) + header.header_bytes;
    if (header.bundle_bytes < header.payload_offset) {
        error = "shard bundle is truncated before payload";
        return false;
    }
    header.payload_bytes = header.bundle_bytes - header.payload_offset;

    if (!json_find_number_u64(header.json, "stage_index", header.stage_index) ||
        !json_find_number_u64(header.json, "layer_start", header.stage_start) ||
        !json_find_number_u64(header.json, "layer_end", header.stage_end) ||
        !json_find_number_u64(header.json, "selected_tensor_count", header.selected_tensor_count) ||
        !json_find_number_u64(header.json, "selected_bytes", header.selected_bytes) ||
        !json_find_bool(header.json, "loadable_gguf", header.loadable_gguf)) {
        error = "shard bundle header is missing required fields";
        return false;
    }
    if (header.payload_bytes != header.selected_bytes) {
        error = "shard bundle payload byte count mismatch";
        return false;
    }
    if (!parse_shard_bundle_tensor_objects(header.json, header.tensors, error)) {
        return false;
    }
    if (header.tensors.size() != header.selected_tensor_count) {
        error = "shard bundle tensor count mismatch";
        return false;
    }
    for (const auto & tensor : header.tensors) {
        if (tensor.bytes > header.payload_bytes || tensor.payload_offset > header.payload_bytes - tensor.bytes) {
            error = "shard bundle tensor payload range is invalid";
            return false;
        }
    }
    return true;
}

static std::string token_to_piece(const llama_vocab * vocab, llama_token token) {
    std::vector<char> piece(32);
    int n_chars = llama_token_to_piece(vocab, token, piece.data(), static_cast<int32_t>(piece.size()), 0, false);
    if (n_chars < 0) {
        piece.resize(static_cast<size_t>(-n_chars));
        n_chars = llama_token_to_piece(vocab, token, piece.data(), static_cast<int32_t>(piece.size()), 0, false);
    }
    if (n_chars <= 0) {
        return "";
    }
    return std::string(piece.data(), static_cast<size_t>(n_chars));
}

static bool parse_shape_spec(const std::string & raw, ShapeSpec & out, std::string & error) {
    if (raw.empty()) {
        error = "--shape is required";
        return false;
    }
    std::stringstream ss(raw);
    std::string part;
    while (std::getline(ss, part, ',')) {
        if (part.empty()) {
            error = "invalid empty shape dimension";
            return false;
        }
        char * end = nullptr;
        const long value = std::strtol(part.c_str(), &end, 10);
        if (end == part.c_str() || *end != '\0' || value <= 0 || value > std::numeric_limits<int>::max()) {
            error = "invalid shape dimension: " + part;
            return false;
        }
        out.dims.push_back(static_cast<int>(value));
    }
    if (out.dims.size() != 2 && out.dims.size() != 3) {
        error = "shape must be [tokens,n_embd] or [1,tokens,n_embd]";
        return false;
    }
    if (out.dims.size() == 3 && out.dims[0] != 1) {
        error = "only one sequence per stage batch is supported";
        return false;
    }
    out.tokens = out.dims[out.dims.size() - 2];
    out.n_embd = out.dims[out.dims.size() - 1];
    return true;
}

static bool activation_to_f32(const std::vector<uint8_t> & payload, const std::string & dtype, const ShapeSpec & shape, std::vector<float> & out, std::string & error) {
    const int64_t element_count = static_cast<int64_t>(shape.tokens) * shape.n_embd;
    if (element_count <= 0) {
        error = "activation shape has no elements";
        return false;
    }
    const std::string normalized = dtype == "float16" ? "f16" : (dtype == "float32" ? "f32" : dtype);
    out.resize(static_cast<size_t>(element_count));
    if (normalized == "f32") {
        const size_t expected = static_cast<size_t>(element_count) * sizeof(float);
        if (payload.size() != expected) {
            error = "f32 activation byte count mismatch";
            return false;
        }
        std::memcpy(out.data(), payload.data(), expected);
        return true;
    }
    if (normalized == "f16") {
        const size_t expected = static_cast<size_t>(element_count) * sizeof(ggml_fp16_t);
        if (payload.size() != expected) {
            error = "f16 activation byte count mismatch";
            return false;
        }
        ggml_fp16_to_fp32_row(reinterpret_cast<const ggml_fp16_t *>(payload.data()), out.data(), element_count);
        return true;
    }
    error = "unsupported activation dtype: " + dtype;
    return false;
}

static std::string model_meta_string(const llama_model * model, const char * key) {
    char buf[512];
    const int32_t n = llama_model_meta_val_str(model, key, buf, sizeof(buf));
    if (n < 0) {
        return "";
    }
    return std::string(buf);
}

static bool parse_layer_tensor_index(const std::string & name, int & layer) {
    static const std::string prefix = "blk.";
    if (name.rfind(prefix, 0) != 0) {
        return false;
    }

    size_t pos = prefix.size();
    size_t end = pos;
    while (end < name.size() && name[end] >= '0' && name[end] <= '9') {
        ++end;
    }
    if (end == pos || end >= name.size() || name[end] != '.') {
        return false;
    }

    layer = std::stoi(name.substr(pos, end - pos));
    return true;
}

static bool is_first_stage_boundary_tensor(const std::string & name) {
    return name.rfind("token_embd.", 0) == 0 ||
           name.rfind("position_embd.", 0) == 0;
}

static bool is_terminal_stage_boundary_tensor(const std::string & name) {
    return name.rfind("output.", 0) == 0 ||
           name.rfind("output_norm.", 0) == 0;
}

static TensorManifest build_tensor_manifest(
    const std::string & model_path,
    int stage_start,
    int stage_end,
    bool first_stage,
    bool terminal_stage
) {
    TensorManifest manifest;
    struct gguf_init_params params = {
        /*.no_alloc =*/ true,
        /*.ctx      =*/ nullptr,
    };

    gguf_context * ctx = gguf_init_from_file(model_path.c_str(), params);
    if (!ctx) {
        return manifest;
    }

    manifest.total_tensor_count = gguf_get_n_tensors(ctx);
    for (int64_t i = 0; i < manifest.total_tensor_count; ++i) {
        const char * raw_name = gguf_get_tensor_name(ctx, i);
        if (!raw_name) {
            continue;
        }

        const std::string name(raw_name);
        int layer = -1;
        const bool is_stage_tensor =
            parse_layer_tensor_index(name, layer) &&
            layer >= stage_start &&
            layer <= stage_end;
        const bool is_boundary_tensor =
            (first_stage && is_first_stage_boundary_tensor(name)) ||
            (terminal_stage && is_terminal_stage_boundary_tensor(name));

        if (!is_stage_tensor && !is_boundary_tensor) {
            continue;
        }

        const uint64_t bytes = static_cast<uint64_t>(gguf_get_tensor_size(ctx, i));
        manifest.selected_bytes += bytes;
        if (is_stage_tensor) {
            manifest.stage_tensor_count++;
        }
        if (is_boundary_tensor) {
            manifest.boundary_tensor_count++;
        }
        const enum ggml_type tensor_type = gguf_get_tensor_type(ctx, i);
        const char * type_name = ggml_type_name(tensor_type);
        manifest.selected.push_back(TensorManifestEntry{
            name,
            type_name ? std::string(type_name) : "",
            bytes,
            is_boundary_tensor,
        });
    }

    gguf_free(ctx);
    return manifest;
}

static void print_tensor_entries(const std::vector<TensorManifestEntry> & entries, size_t limit, const std::string & indent) {
    const size_t n = std::min(entries.size(), limit);
    for (size_t i = 0; i < n; ++i) {
        const auto & entry = entries[i];
        std::cout
            << indent << "{\"name\": \"" << json_escape(entry.name)
            << "\", \"type\": \"" << json_escape(entry.type)
            << "\", \"bytes\": " << entry.bytes
            << ", \"boundary\": " << (entry.boundary ? "true" : "false") << "}";
        if (i + 1 < n) {
            std::cout << ",";
        }
        std::cout << "\n";
    }
}

static void print_tensor_manifest(const TensorManifest & manifest, bool emit_tensor_list, size_t sample_limit) {
    const size_t sample_count = std::min(manifest.selected.size(), sample_limit);
    std::cout
        << "  \"tensor_manifest\": {\n"
        << "    \"source\": \"gguf metadata\",\n"
        << "    \"manifest_only\": true,\n"
        << "    \"total_tensor_count\": " << manifest.total_tensor_count << ",\n"
        << "    \"selected_tensor_count\": " << (manifest.stage_tensor_count + manifest.boundary_tensor_count) << ",\n"
        << "    \"stage_tensor_count\": " << manifest.stage_tensor_count << ",\n"
        << "    \"boundary_tensor_count\": " << manifest.boundary_tensor_count << ",\n"
        << "    \"selected_bytes\": " << manifest.selected_bytes << ",\n"
        << "    \"sample_count\": " << sample_count << ",\n"
        << "    \"sample\": [\n";

    print_tensor_entries(manifest.selected, sample_limit, "      ");

    std::cout
        << "    ]";
    if (emit_tensor_list) {
        std::cout << ",\n"
            << "    \"tensors\": [\n";
        print_tensor_entries(manifest.selected, std::numeric_limits<size_t>::max(), "      ");
        std::cout
            << "    ]\n";
    } else {
        std::cout << "\n";
    }
    std::cout
        << "  },\n";
}

static MaterializationProbe materialize_selected_tensors(const std::string & model_path, const TensorManifest & manifest, int stage_start, int stage_end) {
    MaterializationProbe probe;
    probe.requested = true;

    if (manifest.selected.empty()) {
        probe.status = "skipped";
        probe.error = "selected tensor list is empty";
        return probe;
    }

    std::vector<const char *> allowlist;
    allowlist.reserve(manifest.selected.size());
    for (const auto & tensor : manifest.selected) {
        allowlist.push_back(tensor.name.c_str());
    }

    llama_model_params params = llama_model_default_params();
    params.n_gpu_layers = 0;
    params.use_mmap = true;
    params.cmesh_stage_tensor_allowlist = allowlist.data();
    params.cmesh_stage_tensor_allowlist_count = allowlist.size();
    params.cmesh_stage_layer_start = stage_start;
    params.cmesh_stage_layer_end = stage_end;
    params.cmesh_stage_partial_load = true;

    probe.attempted = true;
    llama_model * selected_model = llama_model_load_from_file(model_path.c_str(), params);
    if (!selected_model) {
        probe.status = "failed";
        probe.error = "llama_model_load_from_file returned null for selected tensor allowlist";
        return probe;
    }
    llama_model_free(selected_model);
    probe.loaded = true;
    probe.status = "loaded";
    return probe;
}

static void print_materialization_probe(const MaterializationProbe & probe, const TensorManifest & manifest) {
    std::cout
        << "  \"materialization_probe\": {\n"
        << "    \"requested\": " << (probe.requested ? "true" : "false") << ",\n"
        << "    \"attempted\": " << (probe.attempted ? "true" : "false") << ",\n"
        << "    \"loaded\": " << (probe.loaded ? "true" : "false") << ",\n"
        << "    \"status\": \"" << json_escape(probe.status) << "\",\n"
        << "    \"selected_tensor_count\": " << (manifest.stage_tensor_count + manifest.boundary_tensor_count) << ",\n"
        << "    \"selected_bytes\": " << manifest.selected_bytes << ",\n"
        << "    \"error\": \"" << json_escape(probe.error) << "\"\n"
        << "  },\n";
}

static int run_write_shard_bundle(const std::vector<std::string> & args) {
    const std::string model_path = arg_value(args, "--model");
    const std::string output_path = arg_value(args, "--output-file");
    const int stage_start = arg_int_value(args, "--stage-start", -1);
    const int stage_end = arg_int_value(args, "--stage-end", -1);
    const int stage_index = arg_int_value(args, "--stage-index", 0);
    const bool first_stage = has_arg(args, "--first-stage");
    const bool terminal_stage = has_arg(args, "--terminal-stage");

    if (model_path.empty()) {
        std::cerr << "--model is required for write-shard-bundle\n";
        return 2;
    }
    if (output_path.empty()) {
        std::cerr << "--output-file is required for write-shard-bundle\n";
        return 2;
    }
    if (stage_start < 0 || stage_end < stage_start) {
        std::cerr << "invalid stage range\n";
        return 2;
    }

    const TensorManifest manifest = build_tensor_manifest(model_path, stage_start, stage_end, first_stage, terminal_stage);
    if (manifest.selected.empty()) {
        std::cerr << "selected tensor manifest is empty\n";
        return 1;
    }

    struct gguf_init_params params = {
        /*.no_alloc =*/ true,
        /*.ctx      =*/ nullptr,
    };
    gguf_context * ctx = gguf_init_from_file(model_path.c_str(), params);
    if (!ctx) {
        std::cerr << "failed to open GGUF metadata: " << model_path << "\n";
        return 1;
    }

    const uint64_t data_offset = static_cast<uint64_t>(gguf_get_data_offset(ctx));
    std::vector<ShardBundleTensor> tensors;
    tensors.reserve(manifest.selected.size());
    uint64_t payload_offset = 0;
    bool missing = false;
    for (const auto & entry : manifest.selected) {
        const int64_t tensor_id = gguf_find_tensor(ctx, entry.name.c_str());
        if (tensor_id < 0) {
            missing = true;
            break;
        }
        const uint64_t tensor_offset = data_offset + static_cast<uint64_t>(gguf_get_tensor_offset(ctx, tensor_id));
        const uint64_t tensor_bytes = static_cast<uint64_t>(gguf_get_tensor_size(ctx, tensor_id));
        tensors.push_back(ShardBundleTensor{
            entry.name,
            entry.type,
            tensor_offset,
            payload_offset,
            tensor_bytes,
            entry.boundary,
        });
        payload_offset += tensor_bytes;
    }
    gguf_free(ctx);
    if (missing) {
        std::cerr << "selected tensor missing from GGUF metadata\n";
        return 1;
    }

    std::error_code ec;
    std::filesystem::create_directories(std::filesystem::path(output_path).parent_path(), ec);

    std::ostringstream header;
    header
        << "{"
        << "\"protocol\":\"cdip.cmesh-shard-bundle-v1\","
        << "\"status\":\"bundle_ready_not_loadable_gguf\","
        << "\"source_model\":\"" << json_escape(model_path) << "\","
        << "\"stage_index\":" << stage_index << ","
        << "\"layer_start\":" << stage_start << ","
        << "\"layer_end\":" << stage_end << ","
        << "\"selected_tensor_count\":" << tensors.size() << ","
        << "\"selected_bytes\":" << payload_offset << ","
        << "\"first_stage\":" << (first_stage ? "true" : "false") << ","
        << "\"terminal_stage\":" << (terminal_stage ? "true" : "false") << ","
        << "\"loadable_gguf\":false,"
        << "\"guardrail\":\"CMesh shard bundle contains selected tensor payloads but is not a standalone llama.cpp GGUF model yet\","
        << "\"tensors\":[";
    for (size_t i = 0; i < tensors.size(); ++i) {
        const auto & tensor = tensors[i];
        header
            << "{\"name\":\"" << json_escape(tensor.name)
            << "\",\"type\":\"" << json_escape(tensor.type)
            << "\",\"source_offset\":" << tensor.source_offset
            << ",\"payload_offset\":" << tensor.payload_offset
            << ",\"bytes\":" << tensor.bytes
            << ",\"boundary\":" << (tensor.boundary ? "true" : "false")
            << "}";
        if (i + 1 < tensors.size()) {
            header << ",";
        }
    }
    header << "]}";
    const std::string header_json = header.str();

    std::ifstream source(model_path, std::ios::binary);
    if (!source) {
        std::cerr << "failed to open source model: " << model_path << "\n";
        return 1;
    }
    std::ofstream out(output_path, std::ios::binary | std::ios::trunc);
    if (!out) {
        std::cerr << "failed to open shard bundle output: " << output_path << "\n";
        return 1;
    }
    const char magic[] = "CMESH_SHARD_BUNDLE_V1\n";
    out.write(magic, sizeof(magic) - 1);
    const uint64_t header_size = static_cast<uint64_t>(header_json.size());
    write_u64_le(out, header_size);
    out.write(header_json.data(), static_cast<std::streamsize>(header_json.size()));
    std::string error;
    for (const auto & tensor : tensors) {
        if (!copy_stream_range(source, out, tensor.source_offset, tensor.bytes, error)) {
            std::cerr << error << "\n";
            return 1;
        }
    }
    out.close();
    if (!out) {
        std::cerr << "failed to finalize shard bundle output\n";
        return 1;
    }

    const uint64_t bundle_bytes = static_cast<uint64_t>(std::filesystem::file_size(output_path, ec));
    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_shard_bundle\",\n"
        << "  \"status\": \"bundle_ready_not_loadable_gguf\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"protocol\": \"cdip.cmesh-shard-bundle-v1\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"output_file\": \"" << json_escape(output_path) << "\",\n"
        << "  \"stage_index\": " << stage_index << ",\n"
        << "  \"stage_start\": " << stage_start << ",\n"
        << "  \"stage_end\": " << stage_end << ",\n"
        << "  \"selected_tensor_count\": " << tensors.size() << ",\n"
        << "  \"selected_bytes\": " << payload_offset << ",\n"
        << "  \"bundle_bytes\": " << bundle_bytes << ",\n"
        << "  \"loadable_gguf\": false,\n"
        << "  \"guardrail\": \"physical tensor payload extraction proof; not a standalone GGUF shard yet\"\n"
        << "}\n";
    return 0;
}

static int run_inspect_shard_bundle(const std::vector<std::string> & args) {
    const std::string bundle_path = arg_value(args, "--bundle-file");
    if (bundle_path.empty()) {
        std::cerr << "--bundle-file is required for inspect-shard-bundle\n";
        return 2;
    }

    ShardBundleHeader header;
    std::string error;
    if (!read_shard_bundle_header(bundle_path, header, error)) {
        std::cerr << error << "\n";
        return 1;
    }
    const ShardBundleTensor * first = header.tensors.empty() ? nullptr : &header.tensors.front();

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_shard_bundle_inspect\",\n"
        << "  \"status\": \"bundle_valid\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"protocol\": \"cdip.cmesh-shard-bundle-v1\",\n"
        << "  \"bundle_file\": \"" << json_escape(bundle_path) << "\",\n"
        << "  \"stage_index\": " << header.stage_index << ",\n"
        << "  \"stage_start\": " << header.stage_start << ",\n"
        << "  \"stage_end\": " << header.stage_end << ",\n"
        << "  \"selected_tensor_count\": " << header.selected_tensor_count << ",\n"
        << "  \"selected_bytes\": " << header.selected_bytes << ",\n"
        << "  \"header_bytes\": " << header.header_bytes << ",\n"
        << "  \"payload_offset\": " << header.payload_offset << ",\n"
        << "  \"payload_bytes\": " << header.payload_bytes << ",\n"
        << "  \"bundle_bytes\": " << header.bundle_bytes << ",\n"
        << "  \"loadable_gguf\": " << (header.loadable_gguf ? "true" : "false") << ",\n"
        << "  \"first_tensor_name\": \"" << json_escape(first ? first->name : "") << "\",\n"
        << "  \"first_tensor_bytes\": " << (first ? first->bytes : 0) << ",\n"
        << "  \"guardrail\": \"valid CMesh shard bundle; standalone llama.cpp GGUF loading is still blocked\"\n"
        << "}\n";
    return 0;
}

static int run_extract_shard_tensor(const std::vector<std::string> & args) {
    const std::string bundle_path = arg_value(args, "--bundle-file");
    const std::string tensor_name = arg_value(args, "--tensor-name");
    const std::string output_path = arg_value(args, "--output-file");
    if (bundle_path.empty()) {
        std::cerr << "--bundle-file is required for extract-shard-tensor\n";
        return 2;
    }
    if (tensor_name.empty()) {
        std::cerr << "--tensor-name is required for extract-shard-tensor\n";
        return 2;
    }
    if (output_path.empty()) {
        std::cerr << "--output-file is required for extract-shard-tensor\n";
        return 2;
    }

    ShardBundleHeader header;
    std::string error;
    if (!read_shard_bundle_header(bundle_path, header, error)) {
        std::cerr << error << "\n";
        return 1;
    }
    const ShardBundleTensor * found = nullptr;
    for (const auto & tensor : header.tensors) {
        if (tensor.name == tensor_name) {
            found = &tensor;
            break;
        }
    }
    if (!found) {
        std::cerr << "tensor not found in shard bundle: " << tensor_name << "\n";
        return 1;
    }

    std::ifstream in(bundle_path, std::ios::binary);
    if (!in) {
        std::cerr << "failed to open shard bundle: " << bundle_path << "\n";
        return 1;
    }
    std::ofstream out(output_path, std::ios::binary | std::ios::trunc);
    if (!out) {
        std::cerr << "failed to open tensor output: " << output_path << "\n";
        return 1;
    }
    const uint64_t absolute_payload_offset = header.payload_offset + found->payload_offset;
    if (!copy_stream_range(in, out, absolute_payload_offset, found->bytes, error)) {
        std::cerr << error << "\n";
        return 1;
    }
    out.close();
    if (!out) {
        std::cerr << "failed to finalize tensor output\n";
        return 1;
    }
    std::error_code ec;
    const uint64_t output_bytes = static_cast<uint64_t>(std::filesystem::file_size(output_path, ec));
    if (ec) {
        std::cerr << "failed to stat tensor output: " << ec.message() << "\n";
        return 1;
    }
    if (output_bytes != found->bytes) {
        std::cerr << "extracted tensor byte count mismatch\n";
        return 1;
    }

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_shard_tensor_extract\",\n"
        << "  \"status\": \"tensor_extracted\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"protocol\": \"cdip.cmesh-shard-bundle-v1\",\n"
        << "  \"bundle_file\": \"" << json_escape(bundle_path) << "\",\n"
        << "  \"tensor_name\": \"" << json_escape(found->name) << "\",\n"
        << "  \"tensor_type\": \"" << json_escape(found->type) << "\",\n"
        << "  \"tensor_bytes\": " << found->bytes << ",\n"
        << "  \"payload_offset\": " << found->payload_offset << ",\n"
        << "  \"absolute_payload_offset\": " << absolute_payload_offset << ",\n"
        << "  \"output_file\": \"" << json_escape(output_path) << "\",\n"
        << "  \"output_bytes\": " << output_bytes << ",\n"
        << "  \"loadable_gguf\": false,\n"
        << "  \"guardrail\": \"single tensor payload extraction proof; not standalone GGUF shard loading yet\"\n"
        << "}\n";
    return 0;
}

static int run_verify_shard_tensor_source(const std::vector<std::string> & args) {
    const std::string bundle_path = arg_value(args, "--bundle-file");
    const std::string model_path = arg_value(args, "--model");
    const std::string tensor_name = arg_value(args, "--tensor-name");
    if (bundle_path.empty()) {
        std::cerr << "--bundle-file is required for verify-shard-tensor-source\n";
        return 2;
    }
    if (model_path.empty()) {
        std::cerr << "--model is required for verify-shard-tensor-source\n";
        return 2;
    }
    if (tensor_name.empty()) {
        std::cerr << "--tensor-name is required for verify-shard-tensor-source\n";
        return 2;
    }

    ShardBundleHeader header;
    std::string error;
    if (!read_shard_bundle_header(bundle_path, header, error)) {
        std::cerr << error << "\n";
        return 1;
    }
    const ShardBundleTensor * found = nullptr;
    for (const auto & tensor : header.tensors) {
        if (tensor.name == tensor_name) {
            found = &tensor;
            break;
        }
    }
    if (!found) {
        std::cerr << "tensor not found in shard bundle: " << tensor_name << "\n";
        return 1;
    }

    struct gguf_init_params params = {
        /*.no_alloc =*/ true,
        /*.ctx      =*/ nullptr,
    };
    gguf_context * ctx = gguf_init_from_file(model_path.c_str(), params);
    if (!ctx) {
        std::cerr << "failed to open GGUF metadata: " << model_path << "\n";
        return 1;
    }
    const int64_t tensor_id = gguf_find_tensor(ctx, tensor_name.c_str());
    if (tensor_id < 0) {
        gguf_free(ctx);
        std::cerr << "tensor not found in source GGUF: " << tensor_name << "\n";
        return 1;
    }
    const uint64_t data_offset = static_cast<uint64_t>(gguf_get_data_offset(ctx));
    const uint64_t source_offset = data_offset + static_cast<uint64_t>(gguf_get_tensor_offset(ctx, tensor_id));
    const uint64_t source_bytes = static_cast<uint64_t>(gguf_get_tensor_size(ctx, tensor_id));
    gguf_free(ctx);
    if (source_bytes != found->bytes) {
        std::cerr << "source tensor byte count differs from shard tensor byte count\n";
        return 1;
    }

    std::ifstream source(model_path, std::ios::binary);
    std::ifstream bundle(bundle_path, std::ios::binary);
    if (!source) {
        std::cerr << "failed to open source model: " << model_path << "\n";
        return 1;
    }
    if (!bundle) {
        std::cerr << "failed to open shard bundle: " << bundle_path << "\n";
        return 1;
    }
    const uint64_t bundle_offset = header.payload_offset + found->payload_offset;
    uint64_t compared_bytes = 0;
    if (!compare_stream_ranges(source, source_offset, bundle, bundle_offset, found->bytes, compared_bytes, error)) {
        std::cerr << error << "\n";
        return 1;
    }

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_shard_tensor_verify\",\n"
        << "  \"status\": \"tensor_source_match\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"protocol\": \"cdip.cmesh-shard-bundle-v1\",\n"
        << "  \"bundle_file\": \"" << json_escape(bundle_path) << "\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"tensor_name\": \"" << json_escape(found->name) << "\",\n"
        << "  \"tensor_type\": \"" << json_escape(found->type) << "\",\n"
        << "  \"tensor_bytes\": " << found->bytes << ",\n"
        << "  \"source_offset\": " << source_offset << ",\n"
        << "  \"bundle_payload_offset\": " << bundle_offset << ",\n"
        << "  \"compared_bytes\": " << compared_bytes << ",\n"
        << "  \"bytes_match\": true,\n"
        << "  \"loadable_gguf\": false,\n"
        << "  \"guardrail\": \"tensor bytes match source GGUF; standalone shard loading is still blocked\"\n"
        << "}\n";
    return 0;
}

static int run_verify_shard_bundle_source(const std::vector<std::string> & args) {
    const std::string bundle_path = arg_value(args, "--bundle-file");
    const std::string model_path = arg_value(args, "--model");
    if (bundle_path.empty()) {
        std::cerr << "--bundle-file is required for verify-shard-bundle-source\n";
        return 2;
    }
    if (model_path.empty()) {
        std::cerr << "--model is required for verify-shard-bundle-source\n";
        return 2;
    }

    ShardBundleHeader header;
    std::string error;
    if (!read_shard_bundle_header(bundle_path, header, error)) {
        std::cerr << error << "\n";
        return 1;
    }

    struct gguf_init_params params = {
        /*.no_alloc =*/ true,
        /*.ctx      =*/ nullptr,
    };
    gguf_context * ctx = gguf_init_from_file(model_path.c_str(), params);
    if (!ctx) {
        std::cerr << "failed to open GGUF metadata: " << model_path << "\n";
        return 1;
    }
    const uint64_t data_offset = static_cast<uint64_t>(gguf_get_data_offset(ctx));

    std::ifstream source(model_path, std::ios::binary);
    std::ifstream bundle(bundle_path, std::ios::binary);
    if (!source) {
        gguf_free(ctx);
        std::cerr << "failed to open source model: " << model_path << "\n";
        return 1;
    }
    if (!bundle) {
        gguf_free(ctx);
        std::cerr << "failed to open shard bundle: " << bundle_path << "\n";
        return 1;
    }

    uint64_t compared_bytes = 0;
    uint64_t verified_tensors = 0;
    for (const auto & tensor : header.tensors) {
        const int64_t tensor_id = gguf_find_tensor(ctx, tensor.name.c_str());
        if (tensor_id < 0) {
            gguf_free(ctx);
            std::cerr << "tensor not found in source GGUF: " << tensor.name << "\n";
            return 1;
        }
        const uint64_t source_offset = data_offset + static_cast<uint64_t>(gguf_get_tensor_offset(ctx, tensor_id));
        const uint64_t source_bytes = static_cast<uint64_t>(gguf_get_tensor_size(ctx, tensor_id));
        if (source_bytes != tensor.bytes) {
            gguf_free(ctx);
            std::cerr << "source tensor byte count differs from shard tensor byte count: " << tensor.name << "\n";
            return 1;
        }
        const uint64_t bundle_offset = header.payload_offset + tensor.payload_offset;
        uint64_t tensor_compared = 0;
        if (!compare_stream_ranges(source, source_offset, bundle, bundle_offset, tensor.bytes, tensor_compared, error)) {
            gguf_free(ctx);
            std::cerr << tensor.name << ": " << error << "\n";
            return 1;
        }
        compared_bytes += tensor_compared;
        ++verified_tensors;
    }
    gguf_free(ctx);
    if (verified_tensors != header.selected_tensor_count || compared_bytes != header.selected_bytes) {
        std::cerr << "bundle source verification aggregate mismatch\n";
        return 1;
    }

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_shard_bundle_verify\",\n"
        << "  \"status\": \"bundle_source_match\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"protocol\": \"cdip.cmesh-shard-bundle-v1\",\n"
        << "  \"bundle_file\": \"" << json_escape(bundle_path) << "\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"stage_index\": " << header.stage_index << ",\n"
        << "  \"stage_start\": " << header.stage_start << ",\n"
        << "  \"stage_end\": " << header.stage_end << ",\n"
        << "  \"verified_tensor_count\": " << verified_tensors << ",\n"
        << "  \"verified_bytes\": " << compared_bytes << ",\n"
        << "  \"selected_tensor_count\": " << header.selected_tensor_count << ",\n"
        << "  \"selected_bytes\": " << header.selected_bytes << ",\n"
        << "  \"bytes_match\": true,\n"
        << "  \"loadable_gguf\": false,\n"
        << "  \"guardrail\": \"all shard tensor bytes match source GGUF; standalone shard loading is still blocked\"\n"
        << "}\n";
    return 0;
}

static int run_write_stage_gguf_shard(const std::vector<std::string> & args) {
    const std::string bundle_path = arg_value(args, "--bundle-file");
    const std::string model_path = arg_value(args, "--model");
    const std::string output_path = arg_value(args, "--output-file");
    if (bundle_path.empty()) {
        std::cerr << "--bundle-file is required for write-stage-gguf-shard\n";
        return 2;
    }
    if (model_path.empty()) {
        std::cerr << "--model is required for write-stage-gguf-shard\n";
        return 2;
    }
    if (output_path.empty()) {
        std::cerr << "--output-file is required for write-stage-gguf-shard\n";
        return 2;
    }

    ShardBundleHeader header;
    std::string error;
    if (!read_shard_bundle_header(bundle_path, header, error)) {
        std::cerr << error << "\n";
        return 1;
    }

    struct ggml_context * source_tensor_ctx = nullptr;
    struct gguf_init_params params = {
        /*.no_alloc =*/ true,
        /*.ctx      =*/ &source_tensor_ctx,
    };
    gguf_context * source_gguf = gguf_init_from_file(model_path.c_str(), params);
    if (!source_gguf || !source_tensor_ctx) {
        if (source_gguf) {
            gguf_free(source_gguf);
        }
        std::cerr << "failed to open source GGUF metadata with tensor context: " << model_path << "\n";
        return 1;
    }

    std::ifstream bundle(bundle_path, std::ios::binary);
    if (!bundle) {
        ggml_free(source_tensor_ctx);
        gguf_free(source_gguf);
        std::cerr << "failed to open shard bundle: " << bundle_path << "\n";
        return 1;
    }

    std::vector<LoadedShardTensor> loaded;
    loaded.reserve(header.tensors.size());
    for (const auto & tensor : header.tensors) {
        ggml_tensor * source_tensor = ggml_get_tensor(source_tensor_ctx, tensor.name.c_str());
        if (!source_tensor) {
            ggml_free(source_tensor_ctx);
            gguf_free(source_gguf);
            std::cerr << "tensor metadata not found in source GGUF: " << tensor.name << "\n";
            return 1;
        }
        const size_t source_nbytes = ggml_nbytes(source_tensor);
        if (source_nbytes != tensor.bytes) {
            ggml_free(source_tensor_ctx);
            gguf_free(source_gguf);
            std::cerr << "tensor metadata byte count mismatch: " << tensor.name << "\n";
            return 1;
        }
        LoadedShardTensor item;
        item.meta = tensor;
        item.data.resize(static_cast<size_t>(tensor.bytes));
        bundle.clear();
        bundle.seekg(static_cast<std::streamoff>(header.payload_offset + tensor.payload_offset), std::ios::beg);
        if (!bundle) {
            ggml_free(source_tensor_ctx);
            gguf_free(source_gguf);
            std::cerr << "failed to seek shard tensor payload: " << tensor.name << "\n";
            return 1;
        }
        if (!item.data.empty()) {
            bundle.read(reinterpret_cast<char *>(item.data.data()), static_cast<std::streamsize>(item.data.size()));
            if (!bundle) {
                ggml_free(source_tensor_ctx);
                gguf_free(source_gguf);
                std::cerr << "failed to read shard tensor payload: " << tensor.name << "\n";
                return 1;
            }
        }
        loaded.push_back(std::move(item));
    }

    gguf_context * out = gguf_init_empty();
    if (!out) {
        ggml_free(source_tensor_ctx);
        gguf_free(source_gguf);
        std::cerr << "failed to create output GGUF context\n";
        return 1;
    }
    gguf_set_kv(out, source_gguf);
    gguf_set_val_str(out, "cmesh.shard.protocol", "cdip.cmesh-stage-gguf-shard-v1");
    gguf_set_val_str(out, "cmesh.shard.source_bundle", bundle_path.c_str());
    gguf_set_val_str(out, "cmesh.shard.source_model", model_path.c_str());
    gguf_set_val_u64(out, "cmesh.shard.stage_index", header.stage_index);
    gguf_set_val_u64(out, "cmesh.shard.layer_start", header.stage_start);
    gguf_set_val_u64(out, "cmesh.shard.layer_end", header.stage_end);
    gguf_set_val_u64(out, "cmesh.shard.selected_tensor_count", header.selected_tensor_count);
    gguf_set_val_u64(out, "cmesh.shard.selected_bytes", header.selected_bytes);
    gguf_set_val_bool(out, "cmesh.shard.loadable_full_model", false);

    for (const auto & item : loaded) {
        ggml_tensor * source_tensor = ggml_get_tensor(source_tensor_ctx, item.meta.name.c_str());
        if (!source_tensor) {
            gguf_free(out);
            ggml_free(source_tensor_ctx);
            gguf_free(source_gguf);
            std::cerr << "tensor metadata disappeared: " << item.meta.name << "\n";
            return 1;
        }
        gguf_add_tensor(out, source_tensor);
        gguf_set_tensor_data(out, item.meta.name.c_str(), item.data.data());
    }

    std::error_code ec;
    std::filesystem::create_directories(std::filesystem::path(output_path).parent_path(), ec);
    if (!gguf_write_to_file(out, output_path.c_str(), false)) {
        gguf_free(out);
        ggml_free(source_tensor_ctx);
        gguf_free(source_gguf);
        std::cerr << "failed to write stage GGUF shard: " << output_path << "\n";
        return 1;
    }
    const uint64_t shard_bytes = static_cast<uint64_t>(std::filesystem::file_size(output_path, ec));

    struct gguf_init_params verify_params = {
        /*.no_alloc =*/ true,
        /*.ctx      =*/ nullptr,
    };
    gguf_context * verify = gguf_init_from_file(output_path.c_str(), verify_params);
    if (!verify) {
        gguf_free(out);
        ggml_free(source_tensor_ctx);
        gguf_free(source_gguf);
        std::cerr << "written stage GGUF shard could not be reopened\n";
        return 1;
    }
    const int64_t verify_tensors = gguf_get_n_tensors(verify);
    gguf_free(verify);

    gguf_free(out);
    ggml_free(source_tensor_ctx);
    gguf_free(source_gguf);

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_gguf_shard\",\n"
        << "  \"status\": \"stage_gguf_shard_ready_not_full_model\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"protocol\": \"cdip.cmesh-stage-gguf-shard-v1\",\n"
        << "  \"bundle_file\": \"" << json_escape(bundle_path) << "\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"output_file\": \"" << json_escape(output_path) << "\",\n"
        << "  \"stage_index\": " << header.stage_index << ",\n"
        << "  \"stage_start\": " << header.stage_start << ",\n"
        << "  \"stage_end\": " << header.stage_end << ",\n"
        << "  \"selected_tensor_count\": " << header.selected_tensor_count << ",\n"
        << "  \"selected_bytes\": " << header.selected_bytes << ",\n"
        << "  \"written_tensor_count\": " << loaded.size() << ",\n"
        << "  \"reopened_tensor_count\": " << verify_tensors << ",\n"
        << "  \"shard_bytes\": " << shard_bytes << ",\n"
        << "  \"loadable_full_model\": false,\n"
        << "  \"guardrail\": \"standalone GGUF container with selected tensors only; llama.cpp full model loading remains blocked until partial loader accepts missing tensors\"\n"
        << "}\n";
    return 0;
}

static int run_probe_stage_gguf_load(const std::vector<std::string> & args) {
    const std::string model_path = arg_value(args, "--model");
    if (model_path.empty()) {
        std::cerr << "--model is required for probe-stage-gguf-load\n";
        return 2;
    }

    StageGGUFLoadPlan plan;
    std::string plan_error;
    if (!load_stage_gguf_plan(model_path, plan, plan_error)) {
        std::cerr << plan_error << "\n";
        return 1;
    }

    llama_model_params params = llama_model_default_params();
    params.n_gpu_layers = 0;
    params.use_mmap = true;
    apply_stage_gguf_plan(params, plan);

    llama_model * model = llama_model_load_from_file(model_path.c_str(), params);
    if (model) {
        const int n_layer = llama_model_n_layer(model);
        const int n_embd = llama_model_n_embd(model);
        const uint64_t model_size = llama_model_size(model);
        llama_model_free(model);

        std::cout
            << "{\n"
            << "  \"kind\": \"cmesh.llamacpp_stage_gguf_load_probe\",\n"
            << "  \"status\": \"stage_model_loaded_partial\",\n"
            << "  \"runtime\": \"llama.cpp\",\n"
            << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
            << "  \"loaded\": true,\n"
            << "  \"cmesh_stage_metadata\": " << (plan.stage_metadata ? "true" : "false") << ",\n"
            << "  \"stage_start\": " << plan.stage_start << ",\n"
            << "  \"stage_end\": " << plan.stage_end << ",\n"
            << "  \"selected_tensor_count\": " << plan.selected_tensor_count << ",\n"
            << "  \"allowlist_tensor_count\": " << plan.tensor_allowlist.size() << ",\n"
            << "  \"n_layer\": " << n_layer << ",\n"
            << "  \"n_embd\": " << n_embd << ",\n"
            << "  \"model_size\": " << model_size << ",\n"
            << "  \"loadable_full_model\": false,\n"
            << "  \"guardrail\": \"stage GGUF loaded through CMesh partial loader metadata and selected tensor allowlist; graph execution still requires stage activation IO validation\"\n"
            << "}\n";
        return 0;
    }

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_gguf_load_probe\",\n"
        << "  \"status\": \"blocked_missing_partial_loader\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"loaded\": false,\n"
        << "  \"cmesh_stage_metadata\": " << (plan.stage_metadata ? "true" : "false") << ",\n"
        << "  \"stage_start\": " << plan.stage_start << ",\n"
        << "  \"stage_end\": " << plan.stage_end << ",\n"
        << "  \"selected_tensor_count\": " << plan.selected_tensor_count << ",\n"
        << "  \"allowlist_tensor_count\": " << plan.tensor_allowlist.size() << ",\n"
        << "  \"loadable_full_model\": false,\n"
        << "  \"required_hook\": \"partial llama.cpp loader must accept selected tensor GGUF shards and bind missing tensors to upstream/downstream stage IO\",\n"
        << "  \"guardrail\": \"expected failure: this selected-tensor GGUF is a physical shard artifact, not a standalone executable model yet\"\n"
        << "}\n";
    return 0;
}

static void print_usage(const char * argv0) {
    std::cerr
        << "Usage:\n"
        << "  " << argv0 << " --probe\n"
        << "  " << argv0 << " --command prepare --model model.gguf --stage-start 0 --stage-end 15 [--stage-index 0] [--emit-tensor-list] [--materialize-selected-tensors]\n"
        << "  " << argv0 << " --command write-shard-bundle --model model.gguf --stage-start 0 --stage-end 15 --output-file stage.cmesh-shard [--first-stage] [--terminal-stage]\n"
        << "  " << argv0 << " --command inspect-shard-bundle --bundle-file stage.cmesh-shard\n"
        << "  " << argv0 << " --command extract-shard-tensor --bundle-file stage.cmesh-shard --tensor-name token_embd.weight --output-file tensor.bin\n"
        << "  " << argv0 << " --command verify-shard-tensor-source --bundle-file stage.cmesh-shard --model model.gguf --tensor-name token_embd.weight\n"
        << "  " << argv0 << " --command verify-shard-bundle-source --bundle-file stage.cmesh-shard --model model.gguf\n"
        << "  " << argv0 << " --command write-stage-gguf-shard --bundle-file stage.cmesh-shard --model model.gguf --output-file stage.gguf\n"
        << "  " << argv0 << " --command probe-stage-gguf-load --model stage.gguf\n"
        << "  " << argv0 << " --command source-decode --model model.gguf --stage-start 0 --stage-end 15 --prompt text --output-file out.bin\n"
        << "  " << argv0 << " --command source-decode --model model.gguf --stage-start 0 --stage-end 15 --token-id 42 --output-file out.bin\n"
        << "  " << argv0 << " --command decode --model model.gguf --stage-start 1 --stage-end 15 --activation-file in.bin --dtype f16 --shape 1,1,896 --output-file out.bin\n"
        << "  " << argv0 << " --command terminal-decode --model model.gguf --stage-start 16 --stage-end 31 --activation-file in.bin --dtype f32 --shape 1,1,896\n"
        << "  " << argv0 << " --command resident-capabilities\n"
        << "  " << argv0 << " --command resident-decode --session-id id --model model.gguf --stage-command relay_decode --activation-file in.bin --dtype f32 --shape 1,1,896 --stage-start 0 --stage-end 15 --stage-index 0 --step 1\n"
        << "  " << argv0 << " --command resident-loop\n"
        << "  " << argv0 << " --command prefill|complete|abort --input stage.json\n\n"
        << "This is the CMesh llama.cpp stage-runner scaffold. It is intentionally blocked\n"
        << "until CMesh wires activation frames into llama.cpp execution and stage-scoped KV hooks.\n";
}

static void print_probe(const std::string & command, const std::string & input) {
    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_runner_probe\",\n"
        << "  \"status\": \"blocked\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"llama_supports_gpu_offload\": " << (llama_supports_gpu_offload() ? "true" : "false") << ",\n"
        << "  \"llama_supports_rpc\": " << (llama_supports_rpc() ? "true" : "false") << ",\n"
        << "  \"command\": \"" << json_escape(command) << "\",\n"
        << "  \"input\": \"" << json_escape(input) << "\",\n"
        << "  \"implemented_hooks\": [\n"
        << "    \"selected tensor materialization plan\",\n"
        << "    \"CMesh shard bundle writer for selected tensor payload extraction\",\n"
        << "    \"CMesh shard bundle reader and payload boundary inspector\",\n"
        << "    \"CMesh shard tensor lookup and payload extractor\",\n"
        << "    \"CMesh shard tensor source byte verifier\",\n"
        << "    \"CMesh shard bundle source byte verifier\",\n"
        << "    \"CMesh selected tensor GGUF shard writer\",\n"
        << "    \"Qwen2 first-stage hidden-state output tensor marker\",\n"
        << "    \"file-based prompt tokens to first-stage hidden-state output bridge\",\n"
        << "    \"Qwen2 middle-stage hidden-state graph input via ubatch.embd\",\n"
        << "    \"file-based activation payload to llama_batch.embd decode bridge\",\n"
        << "    \"local hidden-state output extraction to f32 activation file\",\n"
        << "    \"terminal-stage greedy token export from hidden-state input\",\n"
        << "    \"resident-loop long-lived process transport scaffold\"\n"
        << "  ],\n"
        << "  \"missing_hooks\": [\n"
        << "    \"native model/context construction inside resident-loop\",\n"
        << "    \"resident-loop llama_decode dispatch with per-stage KV ownership\",\n"
        << "    \"remote multi-machine resident stage decode loop\"\n"
        << "  ],\n"
        << "  \"guardrail\": \"not real layer sharding yet\"\n"
        << "}\n";
}

static int run_resident_capabilities() {
    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_resident_capabilities\",\n"
        << "  \"protocol\": \"cdip.llamacpp-resident-runner-v1\",\n"
        << "  \"ready\": true,\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"native_kv\": true,\n"
        << "  \"persistent_model\": true,\n"
        << "  \"persistent_kv_in_memory\": true,\n"
        << "  \"prepare_hook\": true,\n"
        << "  \"decode_hook\": true,\n"
        << "  \"source_decode_hook\": true,\n"
        << "  \"relay_decode_hook\": true,\n"
        << "  \"terminal_decode_hook\": true,\n"
        << "  \"missing_hooks\": [],\n"
        << "  \"blocker\": \"\"\n"
        << "}\n";
    return 0;
}

static int run_resident_decode(const std::vector<std::string> & args) {
    const std::string session_id = arg_value(args, "--session-id");
    const std::string model_path = arg_value(args, "--model");
    const std::string stage_command = arg_value(args, "--stage-command");
    const std::string activation_file = arg_value(args, "--activation-file");
    const std::string dtype = arg_value(args, "--dtype");
    const std::string shape = arg_value(args, "--shape");
    const std::string prompt = arg_value(args, "--prompt");
    const std::string token_text = arg_value(args, "--token-text");
    const int stage_index = arg_int_value(args, "--stage-index", -1);
    const int stage_start = arg_int_value(args, "--stage-start", -1);
    const int stage_end = arg_int_value(args, "--stage-end", -1);
    const int step = arg_int_value(args, "--step", 1);
    const int token_id = arg_int_value(args, "--token-id", -1);
    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_resident_decode\",\n"
        << "  \"status\": \"blocked_missing_native_hooks\",\n"
        << "  \"session_id\": \"" << json_escape(session_id) << "\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"stage_command\": \"" << json_escape(stage_command) << "\",\n"
        << "  \"stage_index\": " << stage_index << ",\n"
        << "  \"stage_start\": " << stage_start << ",\n"
        << "  \"stage_end\": " << stage_end << ",\n"
        << "  \"step\": " << step << ",\n"
        << "  \"activation_file\": \"" << json_escape(activation_file) << "\",\n"
        << "  \"dtype\": \"" << json_escape(dtype) << "\",\n"
        << "  \"shape\": \"" << json_escape(shape) << "\",\n"
        << "  \"prompt_bytes\": " << prompt.size() << ",\n"
        << "  \"token_id\": " << token_id << ",\n"
        << "  \"token_text\": \"" << json_escape(token_text) << "\",\n"
        << "  \"blocker\": \"resident-decode requires native in-process llama.cpp stage context and KV ownership hooks\"\n"
        << "}\n";
    return 3;
}

static void close_resident_loop_session(ResidentLoopSession & session) {
    if (session.ctx != nullptr) {
        llama_free(session.ctx);
        session.ctx = nullptr;
    }
    if (session.model != nullptr) {
        llama_model_free(session.model);
        session.model = nullptr;
    }
    session.native_prepared = false;
}

static bool resident_loop_native_prepare(ResidentLoopSession & session, int n_ctx_arg, std::string & error) {
    StageGGUFLoadPlan stage_plan;
    std::string stage_plan_error;
    if (!load_stage_gguf_plan(session.model_path, stage_plan, stage_plan_error)) {
        error = stage_plan_error;
        return false;
    }
    if (stage_plan.stage_metadata && (stage_plan.stage_start != session.stage_start || stage_plan.stage_end != session.stage_end)) {
        error = "stage range does not match stage GGUF metadata";
        return false;
    }

    llama_model_params metadata_params = llama_model_default_params();
    metadata_params.n_gpu_layers = 0;
    metadata_params.use_mmap = true;
    apply_stage_gguf_plan(metadata_params, stage_plan);
    llama_model * metadata_model = llama_model_load_from_file(session.model_path.c_str(), metadata_params);
    if (!metadata_model) {
        error = "failed to load model metadata: " + session.model_path;
        return false;
    }
    session.n_layer = llama_model_n_layer(metadata_model);
    session.n_embd = llama_model_n_embd(metadata_model);
    const bool valid_range = session.stage_start >= 0 && session.stage_end >= session.stage_start && session.stage_end < session.n_layer;
    const bool first_stage = valid_range && session.stage_start == 0;
    const bool terminal_stage = valid_range && session.stage_end == session.n_layer - 1;
    llama_model_free(metadata_model);
    metadata_model = nullptr;
    if (!valid_range) {
        error = "stage range exceeds model layer count";
        return false;
    }

    const TensorManifest manifest = build_tensor_manifest(session.model_path, session.stage_start, session.stage_end, first_stage, terminal_stage);
    if (manifest.selected.empty()) {
        error = "selected tensor list is empty";
        return false;
    }
    std::vector<const char *> allowlist;
    allowlist.reserve(manifest.selected.size());
    for (const auto & tensor : manifest.selected) {
        allowlist.push_back(tensor.name.c_str());
    }

    llama_model_params model_params = llama_model_default_params();
    model_params.n_gpu_layers = 0;
    model_params.use_mmap = true;
    model_params.cmesh_stage_tensor_allowlist = allowlist.data();
    model_params.cmesh_stage_tensor_allowlist_count = allowlist.size();
    model_params.cmesh_stage_layer_start = session.stage_start;
    model_params.cmesh_stage_layer_end = session.stage_end;
    model_params.cmesh_stage_partial_load = true;
    if (stage_plan.stage_metadata) {
        apply_stage_gguf_plan(model_params, stage_plan);
    }

    llama_model * model = llama_model_load_from_file(session.model_path.c_str(), model_params);
    if (!model) {
        error = "failed to load selected stage model tensors";
        return false;
    }

    llama_context_params ctx_params = llama_context_default_params();
    const int resident_ctx = stage_context_size(n_ctx_arg, 1);
    ctx_params.n_ctx = resident_ctx;
    ctx_params.n_batch = static_cast<uint32_t>(resident_ctx);
    ctx_params.n_ubatch = static_cast<uint32_t>(resident_ctx);
    ctx_params.n_seq_max = 1;
    ctx_params.n_outputs_max = terminal_stage ? 1 : static_cast<uint32_t>(resident_ctx);
    ctx_params.embeddings = !terminal_stage;
    if (!terminal_stage) {
        ctx_params.pooling_type = LLAMA_POOLING_TYPE_NONE;
    }
    llama_context * ctx = llama_init_from_model(model, ctx_params);
    if (!ctx) {
        llama_model_free(model);
        error = "failed to create resident llama context for selected stage";
        return false;
    }

    close_resident_loop_session(session);
    session.model = model;
    session.ctx = ctx;
    session.selected_bytes = manifest.selected_bytes;
    session.selected_tensor_count = static_cast<uint64_t>(manifest.stage_tensor_count + manifest.boundary_tensor_count);
    session.native_prepared = true;
    session.tokens.clear();
    return true;
}

struct ResidentLoopDecodeOutcome {
    std::string status;
    int32_t decode_status = -1;
    uint64_t output_bytes = 0;
    uint64_t token_count = 0;
    int position_offset = 0;
    int next_token_id = -1;
    std::string next_token_text;
    float next_token_logit = 0.0f;
    bool final = false;
    std::string input_mode;
    std::string output_file;
    std::string error;
};

static ResidentLoopDecodeOutcome resident_loop_source_decode(ResidentLoopSession & session, int token_id, const std::string & prompt_file, int step, const std::string & output_file) {
    ResidentLoopDecodeOutcome outcome;
    outcome.output_file = output_file;
    if (!session.native_prepared || session.model == nullptr || session.ctx == nullptr) {
        outcome.status = "blocked_missing_native_prepare";
        outcome.error = "resident session is not native-prepared";
        return outcome;
    }
    if (session.stage_start != 0) {
        outcome.status = "blocked_not_source_stage";
        outcome.error = "source_decode requires a first stage starting at layer 0";
        return outcome;
    }
    const llama_vocab * vocab = llama_model_get_vocab(session.model);
    std::vector<llama_token> decode_tokens;
    if (token_id >= 0) {
        if (token_id >= llama_vocab_n_tokens(vocab)) {
            outcome.status = "invalid_token_id";
            outcome.error = "token_id is outside model vocabulary";
            return outcome;
        }
        decode_tokens.push_back(static_cast<llama_token>(token_id));
        outcome.input_mode = "token_id";
    } else if (!prompt_file.empty()) {
        std::vector<uint8_t> prompt_bytes;
        std::string read_error;
        if (!read_file(prompt_file, prompt_bytes, read_error)) {
            outcome.status = "prompt_file_read_failed";
            outcome.error = read_error;
            return outcome;
        }
        const std::string prompt(prompt_bytes.begin(), prompt_bytes.end());
        int32_t token_count = llama_tokenize(vocab, prompt.c_str(), static_cast<int32_t>(prompt.size()), nullptr, 0, true, true);
        if (token_count < 0) {
            token_count = -token_count;
        }
        if (token_count <= 0) {
            outcome.status = "prompt_tokenize_empty";
            outcome.error = "prompt produced no tokens";
            return outcome;
        }
        decode_tokens.resize(static_cast<size_t>(token_count));
        const int32_t encoded = llama_tokenize(vocab, prompt.c_str(), static_cast<int32_t>(prompt.size()), decode_tokens.data(), token_count, true, true);
        if (encoded < 0 || encoded > token_count) {
            outcome.status = "prompt_tokenize_failed";
            outcome.error = "failed to tokenize prompt";
            return outcome;
        }
        decode_tokens.resize(static_cast<size_t>(encoded));
        outcome.input_mode = "prompt_file";
    } else {
        outcome.status = "invalid_token_id";
        outcome.error = "source_decode requires token_id or prompt_file";
        return outcome;
    }
    const int pos_offset = !session.tokens.empty() ? static_cast<int>(session.tokens.size()) : (token_id >= 0 ? std::max(step - 1, 0) : 0);
    llama_batch batch = llama_batch_init(static_cast<int32_t>(decode_tokens.size()), 0, 1);
    batch.n_tokens = static_cast<int32_t>(decode_tokens.size());
    for (int32_t i = 0; i < batch.n_tokens; ++i) {
        batch.token[i] = decode_tokens[static_cast<size_t>(i)];
        batch.pos[i] = pos_offset + i;
        batch.n_seq_id[i] = 1;
        batch.seq_id[i][0] = 0;
        batch.logits[i] = 1;
    }

    const int32_t decode_status = llama_decode(session.ctx, batch);
    llama_batch_free(batch);
    outcome.decode_status = decode_status;
    if (decode_status != 0) {
        outcome.status = "llama_decode_failed";
        outcome.error = "llama_decode failed";
        return outcome;
    }

    float * embeddings = llama_get_embeddings(session.ctx);
    if (!embeddings) {
        outcome.status = "missing_embeddings";
        outcome.error = "llama_get_embeddings returned null";
        return outcome;
    }
    const size_t output_bytes = decode_tokens.size() * static_cast<size_t>(session.n_embd) * sizeof(float);
    if (!output_file.empty()) {
        std::string write_error;
        if (!write_file(output_file, reinterpret_cast<const uint8_t *>(embeddings), output_bytes, write_error)) {
            outcome.status = "output_write_failed";
            outcome.error = write_error;
            return outcome;
        }
    }
    resize_with_placeholder_tokens(session.tokens, static_cast<size_t>(pos_offset));
    session.tokens.insert(session.tokens.end(), decode_tokens.begin(), decode_tokens.end());
    outcome.status = "resident_source_decoded";
    outcome.output_bytes = output_bytes;
    outcome.token_count = session.tokens.size();
    outcome.position_offset = pos_offset;
    return outcome;
}

static ResidentLoopDecodeOutcome resident_loop_relay_decode(ResidentLoopSession & session, const std::string & activation_file, const std::string & dtype, const std::string & shape_raw, const std::string & output_file) {
    ResidentLoopDecodeOutcome outcome;
    outcome.output_file = output_file;
    outcome.input_mode = "activation_file";
    if (!session.native_prepared || session.model == nullptr || session.ctx == nullptr) {
        outcome.status = "blocked_missing_native_prepare";
        outcome.error = "resident session is not native-prepared";
        return outcome;
    }
    if (activation_file.empty()) {
        outcome.status = "missing_activation_file";
        outcome.error = "relay_decode requires activation_file";
        return outcome;
    }
    if (output_file.empty()) {
        outcome.status = "missing_output_file";
        outcome.error = "relay_decode requires output_file";
        return outcome;
    }

    ShapeSpec shape;
    std::string error;
    if (!parse_shape_spec(shape_raw, shape, error)) {
        outcome.status = "invalid_shape";
        outcome.error = error;
        return outcome;
    }
    if (shape.n_embd != session.n_embd) {
        outcome.status = "invalid_shape";
        outcome.error = "activation n_embd does not match resident model";
        return outcome;
    }
    std::vector<uint8_t> payload;
    if (!read_file(activation_file, payload, error)) {
        outcome.status = "activation_read_failed";
        outcome.error = error;
        return outcome;
    }
    std::vector<float> activation_f32;
    if (!activation_to_f32(payload, dtype, shape, activation_f32, error)) {
        outcome.status = "activation_convert_failed";
        outcome.error = error;
        return outcome;
    }

    const int pos_offset = static_cast<int>(session.tokens.size());
    llama_batch batch = llama_batch_init(shape.tokens, session.n_embd, 1);
    batch.n_tokens = shape.tokens;
    std::memcpy(batch.embd, activation_f32.data(), activation_f32.size() * sizeof(float));
    for (int i = 0; i < shape.tokens; ++i) {
        batch.pos[i] = pos_offset + i;
        batch.n_seq_id[i] = 1;
        batch.seq_id[i][0] = 0;
        batch.logits[i] = 1;
    }

    const int32_t decode_status = llama_decode(session.ctx, batch);
    llama_batch_free(batch);
    outcome.decode_status = decode_status;
    if (decode_status != 0) {
        outcome.status = "llama_decode_failed";
        outcome.error = "llama_decode failed";
        return outcome;
    }

    float * embeddings = llama_get_embeddings(session.ctx);
    if (!embeddings) {
        outcome.status = "missing_embeddings";
        outcome.error = "llama_get_embeddings returned null";
        return outcome;
    }
    const size_t output_bytes = static_cast<size_t>(shape.tokens) * static_cast<size_t>(session.n_embd) * sizeof(float);
    std::string write_error;
    if (!write_file(output_file, reinterpret_cast<const uint8_t *>(embeddings), output_bytes, write_error)) {
        outcome.status = "output_write_failed";
        outcome.error = write_error;
        return outcome;
    }
    resize_with_placeholder_tokens(session.tokens, static_cast<size_t>(pos_offset + shape.tokens));
    outcome.status = "resident_relay_decoded";
    outcome.output_bytes = output_bytes;
    outcome.token_count = session.tokens.size();
    outcome.position_offset = pos_offset;
    return outcome;
}

static ResidentLoopDecodeOutcome resident_loop_terminal_decode(ResidentLoopSession & session, const std::string & activation_file, const std::string & dtype, const std::string & shape_raw) {
    ResidentLoopDecodeOutcome outcome;
    outcome.input_mode = "activation_file";
    if (!session.native_prepared || session.model == nullptr || session.ctx == nullptr) {
        outcome.status = "blocked_missing_native_prepare";
        outcome.error = "resident session is not native-prepared";
        return outcome;
    }
    if (session.stage_end != session.n_layer - 1) {
        outcome.status = "blocked_not_terminal_stage";
        outcome.error = "terminal_decode requires a stage ending at the model terminal layer";
        return outcome;
    }
    if (activation_file.empty()) {
        outcome.status = "missing_activation_file";
        outcome.error = "terminal_decode requires activation_file";
        return outcome;
    }

    ShapeSpec shape;
    std::string error;
    if (!parse_shape_spec(shape_raw, shape, error)) {
        outcome.status = "invalid_shape";
        outcome.error = error;
        return outcome;
    }
    if (shape.n_embd != session.n_embd) {
        outcome.status = "invalid_shape";
        outcome.error = "activation n_embd does not match resident model";
        return outcome;
    }
    std::vector<uint8_t> payload;
    if (!read_file(activation_file, payload, error)) {
        outcome.status = "activation_read_failed";
        outcome.error = error;
        return outcome;
    }
    std::vector<float> activation_f32;
    if (!activation_to_f32(payload, dtype, shape, activation_f32, error)) {
        outcome.status = "activation_convert_failed";
        outcome.error = error;
        return outcome;
    }

    const int pos_offset = static_cast<int>(session.tokens.size());
    llama_batch batch = llama_batch_init(shape.tokens, session.n_embd, 1);
    batch.n_tokens = shape.tokens;
    std::memcpy(batch.embd, activation_f32.data(), activation_f32.size() * sizeof(float));
    for (int i = 0; i < shape.tokens; ++i) {
        batch.pos[i] = pos_offset + i;
        batch.n_seq_id[i] = 1;
        batch.seq_id[i][0] = 0;
        batch.logits[i] = (i == shape.tokens - 1) ? 1 : 0;
    }

    const int32_t decode_status = llama_decode(session.ctx, batch);
    llama_batch_free(batch);
    outcome.decode_status = decode_status;
    if (decode_status != 0) {
        outcome.status = "llama_decode_failed";
        outcome.error = "llama_decode failed";
        return outcome;
    }

    float * logits = llama_get_logits_ith(session.ctx, -1);
    if (!logits) {
        outcome.status = "missing_logits";
        outcome.error = "llama_get_logits_ith returned null";
        return outcome;
    }
    const llama_vocab * vocab = llama_model_get_vocab(session.model);
    const int n_vocab = llama_vocab_n_tokens(vocab);
    int best_token = 0;
    float best_logit = logits[0];
    for (int i = 1; i < n_vocab; ++i) {
        if (logits[i] > best_logit) {
            best_logit = logits[i];
            best_token = i;
        }
    }

    resize_with_placeholder_tokens(session.tokens, static_cast<size_t>(pos_offset + shape.tokens - 1));
    session.tokens.push_back(static_cast<llama_token>(best_token));
    outcome.status = "resident_terminal_decoded";
    outcome.token_count = session.tokens.size();
    outcome.position_offset = pos_offset;
    outcome.next_token_id = best_token;
    outcome.next_token_text = token_to_piece(vocab, static_cast<llama_token>(best_token));
    outcome.next_token_logit = best_logit;
    outcome.final = env_bool_value("CMESH_TERMINAL_FORCE_FINAL", true);
    return outcome;
}

static int run_resident_loop() {
    std::map<std::string, ResidentLoopSession> sessions;
    bool backend_initialized = false;
    std::string line;
    while (std::getline(std::cin, line)) {
        const auto values = parse_kv_line(line);
        const std::string command = kv_string_value(values, "command");

        if (command == "capabilities") {
            std::cout
                << "{"
                << "\"kind\":\"cmesh.llamacpp_resident_loop_capabilities\","
                << "\"protocol\":\"cdip.llamacpp-resident-loop-v1\","
                << "\"runner_protocol\":\"cdip.llamacpp-resident-runner-v1\","
                << "\"ready\":true,"
                << "\"persistent_process\":true,"
                << "\"native_kv\":true,"
                << "\"persistent_model\":true,"
                << "\"persistent_kv_in_memory\":true,"
                << "\"prepare_hook\":true,"
                << "\"decode_hook\":true,"
                << "\"source_decode_hook\":true,"
                << "\"relay_decode_hook\":true,"
                << "\"terminal_decode_hook\":true,"
                << "\"session_count\":" << sessions.size() << ","
                << "\"blocker\":\"resident loop can native-prepare a stage model/context and run source/relay/terminal decode; full multi-worker token loop validation is still pending\""
                << "}\n";
            std::cout.flush();
            continue;
        }

        if (command == "prepare") {
            ResidentLoopSession session;
            session.session_id = kv_string_value(values, "session_id");
            session.model_path = kv_string_value(values, "model");
            session.stage_index = kv_int_value(values, "stage_index", -1);
            session.stage_start = kv_int_value(values, "stage_start", -1);
            session.stage_end = kv_int_value(values, "stage_end", -1);
            const bool native_prepare = kv_string_value(values, "native_prepare") == "1" || kv_string_value(values, "native_prepare") == "true";
            const int n_ctx_arg = kv_int_value(values, "ctx", 0);
            const bool valid = !session.session_id.empty() && !session.model_path.empty() && session.stage_start >= 0 && session.stage_end >= session.stage_start;
            std::string status = valid ? "blocked_missing_native_prepare_hooks" : "invalid_request";
            std::string blocker = "native model/context construction must move into this resident loop before prepare can become ready";
            std::string error;
            if (valid && native_prepare) {
                if (!backend_initialized) {
                    llama_backend_init();
                    backend_initialized = true;
                }
                if (resident_loop_native_prepare(session, n_ctx_arg, error)) {
                    status = "resident_ready";
                    blocker = "";
                } else {
                    status = "native_prepare_failed";
                    blocker = error;
                }
            }
            if (valid) {
                auto existing = sessions.find(session.session_id);
                if (existing != sessions.end()) {
                    close_resident_loop_session(existing->second);
                }
                sessions[session.session_id] = session;
            }
            std::cout
                << "{"
                << "\"kind\":\"cmesh.llamacpp_resident_loop_prepare\","
                << "\"protocol\":\"cdip.llamacpp-resident-loop-v1\","
                << "\"status\":\"" << json_escape(status) << "\","
                << "\"session_registered\":" << (valid ? "true" : "false") << ","
                << "\"session_id\":\"" << json_escape(session.session_id) << "\","
                << "\"model_path\":\"" << json_escape(session.model_path) << "\","
                << "\"stage_index\":" << session.stage_index << ","
                << "\"stage_start\":" << session.stage_start << ","
                << "\"stage_end\":" << session.stage_end << ","
                << "\"session_count\":" << sessions.size() << ","
                << "\"native_prepare_requested\":" << (native_prepare ? "true" : "false") << ","
                << "\"persistent_model\":" << (session.native_prepared ? "true" : "false") << ","
                << "\"persistent_kv_in_memory\":" << (session.native_prepared ? "true" : "false") << ","
                << "\"n_layer\":" << session.n_layer << ","
                << "\"n_embd\":" << session.n_embd << ","
                << "\"selected_tensor_count\":" << session.selected_tensor_count << ","
                << "\"selected_bytes\":" << session.selected_bytes << ","
                << "\"blocker\":\"" << json_escape(blocker) << "\""
                << "}\n";
            std::cout.flush();
            continue;
        }

        if (command == "decode") {
            const std::string session_id = kv_string_value(values, "session_id");
            const int step = kv_int_value(values, "step", 0);
            const std::string stage_command = kv_string_value(values, "stage_command");
            const std::string activation_file = kv_string_value(values, "activation_file");
            const std::string output_file = kv_string_value(values, "output_file");
            const std::string prompt_file = kv_string_value(values, "prompt_file");
            const std::string dtype = kv_string_value(values, "dtype");
            const std::string shape = kv_string_value(values, "shape");
            const int token_id = kv_int_value(values, "token_id", -1);
            uint64_t payload_bytes = 0;
            if (!activation_file.empty()) {
                std::error_code ec;
                payload_bytes = std::filesystem::file_size(activation_file, ec);
                if (ec) {
                    payload_bytes = 0;
                }
            }
            auto it = sessions.find(session_id);
            const bool found = it != sessions.end();
            if (found) {
                it->second.decode_steps += 1;
                it->second.last_step = step;
                it->second.last_stage_command = stage_command;
            }
            ResidentLoopDecodeOutcome decode_outcome;
            decode_outcome.status = found ? "blocked_missing_native_decode_hooks" : "unknown_session";
            if (found && stage_command == "source_decode" && it->second.native_prepared) {
                decode_outcome = resident_loop_source_decode(it->second, token_id, prompt_file, step, output_file);
            } else if (found && stage_command == "relay_decode" && it->second.native_prepared) {
                decode_outcome = resident_loop_relay_decode(it->second, activation_file, dtype, shape, output_file);
            } else if (found && stage_command == "terminal_decode" && it->second.native_prepared) {
                decode_outcome = resident_loop_terminal_decode(it->second, activation_file, dtype, shape);
            }
            std::cout
                << "{"
                << "\"kind\":\"cmesh.llamacpp_resident_loop_decode\","
                << "\"protocol\":\"cdip.llamacpp-resident-loop-v1\","
                << "\"status\":\"" << json_escape(decode_outcome.status) << "\","
                << "\"session_id\":\"" << json_escape(session_id) << "\","
                << "\"stage_command\":\"" << json_escape(stage_command) << "\","
                << "\"step\":" << step << ","
                << "\"session_found\":" << (found ? "true" : "false") << ","
                << "\"decode_steps\":" << (found ? it->second.decode_steps : 0) << ","
                << "\"persistent_model\":" << (found && it->second.native_prepared ? "true" : "false") << ","
                << "\"persistent_kv_in_memory\":" << (found && it->second.native_prepared ? "true" : "false") << ","
                << "\"activation_file\":\"" << json_escape(activation_file) << "\","
                << "\"payload_bytes\":" << payload_bytes << ","
                << "\"output_file\":\"" << json_escape(output_file) << "\","
                << "\"output_bytes\":" << decode_outcome.output_bytes << ","
                << "\"dtype\":\"" << json_escape(dtype) << "\","
                << "\"shape\":\"" << json_escape(shape) << "\","
                << "\"token_id\":" << token_id << ","
                << "\"input_mode\":\"" << json_escape(decode_outcome.input_mode) << "\","
                << "\"position_offset\":" << decode_outcome.position_offset << ","
                << "\"token_count\":" << decode_outcome.token_count << ","
                << "\"next_token_id\":" << decode_outcome.next_token_id << ","
                << "\"next_token_text\":\"" << json_escape(decode_outcome.next_token_text) << "\","
                << "\"next_token_logit\":" << decode_outcome.next_token_logit << ","
                << "\"final\":" << (decode_outcome.final ? "true" : "false") << ","
                << "\"decode_status\":" << decode_outcome.decode_status << ","
                << "\"error\":\"" << json_escape(decode_outcome.error) << "\","
                << "\"blocker\":\"" << (decode_outcome.status == "resident_source_decoded" || decode_outcome.status == "resident_relay_decoded" || decode_outcome.status == "resident_terminal_decoded" ? "" : "decode dispatch reached the resident process, but this stage command is not ready") << "\""
                << "}\n";
            std::cout.flush();
            continue;
        }

        if (command == "complete" || command == "abort") {
            const std::string session_id = kv_string_value(values, "session_id");
            auto it = sessions.find(session_id);
            if (it != sessions.end()) {
                close_resident_loop_session(it->second);
            }
            const size_t erased = sessions.erase(session_id);
            std::cout
                << "{"
                << "\"kind\":\"cmesh.llamacpp_resident_loop_session_close\","
                << "\"protocol\":\"cdip.llamacpp-resident-loop-v1\","
                << "\"command\":\"" << json_escape(command) << "\","
                << "\"session_id\":\"" << json_escape(session_id) << "\","
                << "\"closed\":" << (erased > 0 ? "true" : "false") << ","
                << "\"session_count\":" << sessions.size()
                << "}\n";
            std::cout.flush();
            continue;
        }

        if (command == "shutdown") {
            for (auto & entry : sessions) {
                close_resident_loop_session(entry.second);
            }
            std::cout
                << "{"
                << "\"kind\":\"cmesh.llamacpp_resident_loop_shutdown\","
                << "\"protocol\":\"cdip.llamacpp-resident-loop-v1\","
                << "\"status\":\"closing\","
                << "\"session_count\":" << sessions.size()
                << "}\n";
            std::cout.flush();
            if (backend_initialized) {
                llama_backend_free();
            }
            return 0;
        }

        std::cout
            << "{"
            << "\"kind\":\"cmesh.llamacpp_resident_loop_error\","
            << "\"protocol\":\"cdip.llamacpp-resident-loop-v1\","
            << "\"status\":\"unsupported_command\","
            << "\"command\":\"" << json_escape(command) << "\""
            << "}\n";
        std::cout.flush();
    }
    for (auto & entry : sessions) {
        close_resident_loop_session(entry.second);
    }
    if (backend_initialized) {
        llama_backend_free();
    }
    return 0;
}

static int run_prepare(const std::vector<std::string> & args) {
    const std::string model_path = arg_value(args, "--model");
    const int stage_start = arg_int_value(args, "--stage-start", -1);
    const int stage_end = arg_int_value(args, "--stage-end", -1);
    const int stage_index = arg_int_value(args, "--stage-index", 0);
    const bool emit_tensor_list = has_arg(args, "--emit-tensor-list");
    const bool materialize_selected = has_arg(args, "--materialize-selected-tensors");
    const size_t manifest_sample_limit = arg_size_value(args, "--manifest-sample-limit", 16);

    if (model_path.empty()) {
        std::cerr << "--model is required for prepare\n";
        return 2;
    }
    if (stage_start < 0 || stage_end < stage_start) {
        std::cerr << "invalid stage range\n";
        return 2;
    }

    llama_model_params params = llama_model_default_params();
    params.n_gpu_layers = 0;
    params.use_mmap = true;

    llama_model * model = llama_model_load_from_file(model_path.c_str(), params);
    if (!model) {
        std::cerr << "failed to load model: " << model_path << "\n";
        return 1;
    }

    char desc[512];
    const int32_t desc_len = llama_model_desc(model, desc, sizeof(desc));
    const std::string model_desc = desc_len >= 0 ? std::string(desc) : "";
    const int n_layer = llama_model_n_layer(model);
    const int n_embd = llama_model_n_embd(model);
    const int n_embd_inp = llama_model_n_embd_inp(model);
    const int n_embd_out = llama_model_n_embd_out(model);
    const int n_ctx_train = llama_model_n_ctx_train(model);
    const uint64_t model_size = llama_model_size(model);
    const uint64_t n_params = llama_model_n_params(model);
    const std::string arch = model_meta_string(model, "general.architecture");
    const std::string name = model_meta_string(model, "general.name");

    const bool valid_range = stage_end < n_layer;
    const bool first_stage = valid_range && stage_start == 0;
    const bool terminal_stage = valid_range && stage_end == n_layer - 1;
    const TensorManifest manifest = valid_range
        ? build_tensor_manifest(model_path, stage_start, stage_end, first_stage, terminal_stage)
        : TensorManifest{};
    llama_model_free(model);
    model = nullptr;

    MaterializationProbe materialization_probe;
    if (valid_range && materialize_selected) {
        materialization_probe = materialize_selected_tensors(model_path, manifest, stage_start, stage_end);
    }

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_prepare\",\n"
        << "  \"status\": \"" << (valid_range ? "metadata_ready" : "blocked") << "\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"model_name\": \"" << json_escape(name) << "\",\n"
        << "  \"model_architecture\": \"" << json_escape(arch) << "\",\n"
        << "  \"model_description\": \"" << json_escape(model_desc) << "\",\n"
        << "  \"model_size_bytes\": " << model_size << ",\n"
        << "  \"model_parameters\": " << n_params << ",\n"
        << "  \"n_layer\": " << n_layer << ",\n"
        << "  \"n_embd\": " << n_embd << ",\n"
        << "  \"n_embd_inp\": " << n_embd_inp << ",\n"
        << "  \"n_embd_out\": " << n_embd_out << ",\n"
        << "  \"n_ctx_train\": " << n_ctx_train << ",\n"
        << "  \"stage_index\": " << stage_index << ",\n"
        << "  \"stage_start\": " << stage_start << ",\n"
        << "  \"stage_end\": " << stage_end << ",\n"
        << "  \"stage_layer_count\": " << (valid_range ? stage_end - stage_start + 1 : 0) << ",\n"
        << "  \"first_stage\": " << (first_stage ? "true" : "false") << ",\n"
        << "  \"terminal_stage\": " << (terminal_stage ? "true" : "false") << ",\n"
        << "  \"expected_hidden_state\": {\n"
        << "    \"dtype\": \"f16\",\n"
        << "    \"shape_template\": [1, \"tokens\", " << n_embd << "]\n"
        << "  },\n";
    print_tensor_manifest(manifest, emit_tensor_list, manifest_sample_limit);
    print_materialization_probe(materialization_probe, manifest);
    std::cout
        << "  \"selected_tensor_materialization_ready\": " << (materialization_probe.loaded ? "true" : "false") << ",\n"
        << "  \"implemented_hooks\": [\n"
        << "    \"selected tensor materialization plan\",\n"
        << "    \"Qwen2 first-stage hidden-state output tensor marker\",\n"
        << "    \"file-based prompt tokens to first-stage hidden-state output bridge\",\n"
        << "    \"Qwen2 middle-stage hidden-state graph input via ubatch.embd\",\n"
        << "    \"file-based activation payload to llama_batch.embd decode bridge\",\n"
        << "    \"local hidden-state output extraction to f32 activation file\"\n"
        << "  ],\n"
        << "  \"executable\": false,\n"
        << "  \"missing_hooks\": [\n"
        << "    \"CDIP relay activation frame to stage-runner decode bridge\",\n"
        << "    \"daemonized stage session process\",\n"
        << "    \"terminal stage logits export\",\n"
        << "    \"remote stage decode loop\"\n"
        << "  ],\n"
        << "  \"guardrail\": \"metadata prepare only; not real layer sharding yet\"\n"
        << "}\n";

    return valid_range ? 0 : 3;
}

static int run_decode(const std::vector<std::string> & args) {
    const std::string model_path = arg_value(args, "--model");
    const std::string activation_path = arg_value(args, "--activation-file");
    const std::string output_path = arg_value(args, "--output-file");
    const std::string dtype = arg_value(args, "--dtype", "f16");
    const std::string shape_raw = arg_value(args, "--shape");
    const int stage_start = arg_int_value(args, "--stage-start", -1);
    const int stage_end = arg_int_value(args, "--stage-end", -1);
    const int stage_index = arg_int_value(args, "--stage-index", 0);
    const int n_threads = arg_int_value(args, "--threads", 0);
    const int n_ctx_arg = arg_int_value(args, "--ctx", 0);

    if (model_path.empty()) {
        std::cerr << "--model is required for decode\n";
        return 2;
    }
    if (activation_path.empty()) {
        std::cerr << "--activation-file is required for decode\n";
        return 2;
    }
    if (output_path.empty()) {
        std::cerr << "--output-file is required for decode\n";
        return 2;
    }
    if (stage_start < 0 || stage_end < stage_start) {
        std::cerr << "invalid stage range\n";
        return 2;
    }

    ShapeSpec shape;
    std::string error;
    if (!parse_shape_spec(shape_raw, shape, error)) {
        std::cerr << error << "\n";
        return 2;
    }

    std::vector<uint8_t> payload;
    if (!read_file(activation_path, payload, error)) {
        std::cerr << error << "\n";
        return 2;
    }

    std::vector<float> activation_f32;
    if (!activation_to_f32(payload, dtype, shape, activation_f32, error)) {
        std::cerr << error << "\n";
        return 2;
    }

    llama_backend_init();

    StageGGUFLoadPlan stage_plan;
    std::string stage_plan_error;
    if (!load_stage_gguf_plan(model_path, stage_plan, stage_plan_error)) {
        llama_backend_free();
        std::cerr << stage_plan_error << "\n";
        return 1;
    }
    if (stage_plan.stage_metadata && (stage_plan.stage_start != stage_start || stage_plan.stage_end != stage_end)) {
        llama_backend_free();
        std::cerr << "stage range does not match stage GGUF metadata\n";
        return 2;
    }

    llama_model_params metadata_params = llama_model_default_params();
    metadata_params.n_gpu_layers = 0;
    metadata_params.use_mmap = true;
    apply_stage_gguf_plan(metadata_params, stage_plan);
    llama_model * metadata_model = llama_model_load_from_file(model_path.c_str(), metadata_params);
    if (!metadata_model) {
        llama_backend_free();
        std::cerr << "failed to load model metadata: " << model_path << "\n";
        return 1;
    }
    const int n_layer = llama_model_n_layer(metadata_model);
    const int n_embd = llama_model_n_embd(metadata_model);
    const bool valid_range = stage_end < n_layer;
    const bool first_stage = valid_range && stage_start == 0;
    const bool terminal_stage = valid_range && stage_end == n_layer - 1;
    llama_model_free(metadata_model);
    metadata_model = nullptr;

    if (!valid_range) {
        llama_backend_free();
        std::cerr << "stage range exceeds model layer count\n";
        return 2;
    }
    if (shape.n_embd != n_embd) {
        llama_backend_free();
        std::cerr << "activation n_embd does not match model: activation=" << shape.n_embd << " model=" << n_embd << "\n";
        return 2;
    }

    const TensorManifest manifest = build_tensor_manifest(model_path, stage_start, stage_end, first_stage, terminal_stage);
    if (manifest.selected.empty()) {
        llama_backend_free();
        std::cerr << "selected tensor list is empty\n";
        return 2;
    }

    std::vector<const char *> allowlist;
    allowlist.reserve(manifest.selected.size());
    for (const auto & tensor : manifest.selected) {
        allowlist.push_back(tensor.name.c_str());
    }

    llama_model_params model_params = llama_model_default_params();
    model_params.n_gpu_layers = 0;
    model_params.use_mmap = true;
    model_params.cmesh_stage_tensor_allowlist = allowlist.data();
    model_params.cmesh_stage_tensor_allowlist_count = allowlist.size();
    model_params.cmesh_stage_layer_start = stage_start;
    model_params.cmesh_stage_layer_end = stage_end;
    model_params.cmesh_stage_partial_load = true;
    if (stage_plan.stage_metadata) {
        apply_stage_gguf_plan(model_params, stage_plan);
    }

    llama_model * model = llama_model_load_from_file(model_path.c_str(), model_params);
    if (!model) {
        llama_backend_free();
        std::cerr << "failed to load selected stage model tensors\n";
        return 1;
    }

    llama_context_params ctx_params = llama_context_default_params();
    ctx_params.n_ctx = stage_context_size(n_ctx_arg, std::max(shape.tokens, 1));
    ctx_params.n_batch = static_cast<uint32_t>(shape.tokens);
    ctx_params.n_ubatch = static_cast<uint32_t>(shape.tokens);
    ctx_params.n_seq_max = 1;
    ctx_params.n_outputs_max = static_cast<uint32_t>(shape.tokens);
    ctx_params.embeddings = true;
    ctx_params.pooling_type = LLAMA_POOLING_TYPE_NONE;
    if (n_threads > 0) {
        ctx_params.n_threads = n_threads;
        ctx_params.n_threads_batch = n_threads;
    }

    llama_context * ctx = llama_init_from_model(model, ctx_params);
    if (!ctx) {
        llama_model_free(model);
        llama_backend_free();
        std::cerr << "failed to create llama context for stage decode\n";
        return 1;
    }

    const std::string session_path = stage_session_file();
    std::vector<llama_token> session_tokens;
    std::string session_error;
    const size_t session_loaded_bytes = load_stage_sequence_state(ctx, session_path, session_tokens, session_error);
    if (!session_error.empty()) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << session_error << "\n";
        return 1;
    }
    const int pos_offset = next_sequence_position(ctx, session_tokens);

    llama_batch batch = llama_batch_init(shape.tokens, n_embd, 1);
    batch.n_tokens = shape.tokens;
    std::memcpy(batch.embd, activation_f32.data(), activation_f32.size() * sizeof(float));
    for (int i = 0; i < shape.tokens; ++i) {
        batch.pos[i] = pos_offset + i;
        batch.n_seq_id[i] = 1;
        batch.seq_id[i][0] = 0;
        batch.logits[i] = 1;
    }

    const int32_t decode_status = llama_decode(ctx, batch);
    llama_batch_free(batch);
    if (decode_status != 0) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << "llama_decode failed: " << decode_status << "\n";
        return 1;
    }

    float * embeddings = llama_get_embeddings(ctx);
    if (!embeddings) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << "llama_get_embeddings returned null\n";
        return 1;
    }

    const size_t output_bytes = static_cast<size_t>(shape.tokens) * static_cast<size_t>(n_embd) * sizeof(float);
    if (!write_file(output_path, reinterpret_cast<const uint8_t *>(embeddings), output_bytes, error)) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << error << "\n";
        return 1;
    }

    resize_with_placeholder_tokens(session_tokens, static_cast<size_t>(pos_offset + shape.tokens));
    const size_t session_saved_bytes = save_stage_sequence_state(ctx, session_path, session_tokens, session_error);
    if (!session_error.empty()) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << session_error << "\n";
        return 1;
    }

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_decode\",\n"
        << "  \"status\": \"executed\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"stage_index\": " << stage_index << ",\n"
        << "  \"stage_start\": " << stage_start << ",\n"
        << "  \"stage_end\": " << stage_end << ",\n"
        << "  \"terminal_stage\": " << (terminal_stage ? "true" : "false") << ",\n"
        << "  \"input_tensor\": {\n"
        << "    \"dtype\": \"" << json_escape(dtype) << "\",\n"
        << "    \"shape\": [";
    for (size_t i = 0; i < shape.dims.size(); ++i) {
        if (i > 0) {
            std::cout << ", ";
        }
        std::cout << shape.dims[i];
    }
    std::cout
        << "],\n"
        << "    \"bytes\": " << payload.size() << ",\n"
        << "    \"llama_batch_dtype\": \"f32\",\n"
        << "    \"llama_batch_bytes\": " << (activation_f32.size() * sizeof(float)) << "\n"
        << "  },\n"
        << "  \"output_tensor\": {\n"
        << "    \"dtype\": \"f32\",\n"
        << "    \"shape\": [1, " << shape.tokens << ", " << n_embd << "],\n"
        << "    \"bytes\": " << output_bytes << ",\n"
        << "    \"path\": \"" << json_escape(output_path) << "\"\n"
        << "  },\n"
        << "  \"selected_tensor_count\": " << (manifest.stage_tensor_count + manifest.boundary_tensor_count) << ",\n"
        << "  \"selected_bytes\": " << manifest.selected_bytes << ",\n"
        << "  \"kv_session\": {\n"
        << "    \"enabled\": " << (!session_path.empty() ? "true" : "false") << ",\n"
        << "    \"path\": \"" << json_escape(session_path) << "\",\n"
        << "    \"loaded_bytes\": " << session_loaded_bytes << ",\n"
        << "    \"saved_bytes\": " << session_saved_bytes << ",\n"
        << "    \"token_count\": " << session_tokens.size() << ",\n"
        << "    \"position_offset\": " << pos_offset << "\n"
        << "  },\n"
        << "  \"decode_status\": " << decode_status << ",\n"
        << "  \"guardrail\": \"first executable local llama.cpp stage bridge; file-backed native sequence state enabled when CMESH_STAGE_SESSION_FILE is set; daemonized KV ownership still pending\"\n"
        << "}\n";

    llama_free(ctx);
    llama_model_free(model);
    llama_backend_free();
    return 0;
}

static int run_terminal_decode(const std::vector<std::string> & args) {
    const std::string model_path = arg_value(args, "--model");
    const std::string activation_path = arg_value(args, "--activation-file");
    const std::string dtype = arg_value(args, "--dtype");
    const std::string shape_raw = arg_value(args, "--shape");
    const int stage_start = arg_int_value(args, "--stage-start", -1);
    const int stage_end = arg_int_value(args, "--stage-end", -1);
    const int stage_index = arg_int_value(args, "--stage-index", 0);
    const int n_threads = arg_int_value(args, "--threads", 0);
    const int n_ctx_arg = arg_int_value(args, "--ctx", 0);

    if (model_path.empty()) {
        std::cerr << "--model is required for terminal-decode\n";
        return 2;
    }
    if (activation_path.empty()) {
        std::cerr << "--activation-file is required for terminal-decode\n";
        return 2;
    }
    if (stage_start < 0 || stage_end < stage_start) {
        std::cerr << "valid --stage-start and --stage-end are required\n";
        return 2;
    }

    ShapeSpec shape;
    std::string error;
    if (!parse_shape_spec(shape_raw, shape, error)) {
        std::cerr << error << "\n";
        return 2;
    }
    std::vector<uint8_t> payload;
    if (!read_file(activation_path, payload, error)) {
        std::cerr << error << "\n";
        return 1;
    }
    std::vector<float> activation_f32;
    if (!activation_to_f32(payload, dtype, shape, activation_f32, error)) {
        std::cerr << error << "\n";
        return 2;
    }

    llama_backend_init();

    StageGGUFLoadPlan stage_plan;
    std::string stage_plan_error;
    if (!load_stage_gguf_plan(model_path, stage_plan, stage_plan_error)) {
        llama_backend_free();
        std::cerr << stage_plan_error << "\n";
        return 1;
    }
    if (stage_plan.stage_metadata && (stage_plan.stage_start != stage_start || stage_plan.stage_end != stage_end)) {
        llama_backend_free();
        std::cerr << "source-decode stage range does not match stage GGUF metadata\n";
        return 2;
    }

    llama_model_params metadata_params = llama_model_default_params();
    metadata_params.n_gpu_layers = 0;
    metadata_params.use_mmap = true;
    apply_stage_gguf_plan(metadata_params, stage_plan);
    llama_model * metadata_model = llama_model_load_from_file(model_path.c_str(), metadata_params);
    if (!metadata_model) {
        llama_backend_free();
        std::cerr << "failed to load model metadata: " << model_path << "\n";
        return 1;
    }
    const int n_layer = llama_model_n_layer(metadata_model);
    const int n_embd = llama_model_n_embd(metadata_model);
    const llama_vocab * metadata_vocab = llama_model_get_vocab(metadata_model);
    const int n_vocab = llama_vocab_n_tokens(metadata_vocab);
    const bool terminal_stage = stage_end == n_layer - 1;
    if (!terminal_stage) {
        llama_model_free(metadata_model);
        llama_backend_free();
        std::cerr << "terminal-decode requires a stage ending at the model terminal layer\n";
        return 2;
    }
    if (shape.n_embd != n_embd) {
        llama_model_free(metadata_model);
        llama_backend_free();
        std::cerr << "activation embedding dimension does not match model n_embd\n";
        return 2;
    }
    llama_model_free(metadata_model);
    metadata_model = nullptr;

    const TensorManifest manifest = build_tensor_manifest(model_path, stage_start, stage_end, false, true);
    if (manifest.selected.empty()) {
        llama_backend_free();
        std::cerr << "selected tensor list is empty\n";
        return 2;
    }

    std::vector<const char *> allowlist;
    allowlist.reserve(manifest.selected.size());
    for (const auto & tensor : manifest.selected) {
        allowlist.push_back(tensor.name.c_str());
    }

    llama_model_params model_params = llama_model_default_params();
    model_params.n_gpu_layers = 0;
    model_params.use_mmap = true;
    model_params.cmesh_stage_tensor_allowlist = allowlist.data();
    model_params.cmesh_stage_tensor_allowlist_count = allowlist.size();
    model_params.cmesh_stage_layer_start = stage_start;
    model_params.cmesh_stage_layer_end = stage_end;
    model_params.cmesh_stage_partial_load = true;
    if (stage_plan.stage_metadata) {
        apply_stage_gguf_plan(model_params, stage_plan);
    }

    llama_model * model = llama_model_load_from_file(model_path.c_str(), model_params);
    if (!model) {
        llama_backend_free();
        std::cerr << "failed to load selected terminal stage tensors\n";
        return 1;
    }
    const llama_vocab * vocab = llama_model_get_vocab(model);

    llama_context_params ctx_params = llama_context_default_params();
    ctx_params.n_ctx = stage_context_size(n_ctx_arg, std::max(shape.tokens, 1));
    ctx_params.n_batch = static_cast<uint32_t>(shape.tokens);
    ctx_params.n_ubatch = static_cast<uint32_t>(shape.tokens);
    ctx_params.n_seq_max = 1;
    ctx_params.n_outputs_max = 1;
    ctx_params.embeddings = false;
    if (n_threads > 0) {
        ctx_params.n_threads = n_threads;
        ctx_params.n_threads_batch = n_threads;
    }

    llama_context * ctx = llama_init_from_model(model, ctx_params);
    if (!ctx) {
        llama_model_free(model);
        llama_backend_free();
        std::cerr << "failed to create llama context for terminal decode\n";
        return 1;
    }

    const std::string session_path = stage_session_file();
    std::vector<llama_token> session_tokens;
    std::string session_error;
    const size_t session_loaded_bytes = load_stage_sequence_state(ctx, session_path, session_tokens, session_error);
    if (!session_error.empty()) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << session_error << "\n";
        return 1;
    }
    const int pos_offset = next_sequence_position(ctx, session_tokens);

    llama_batch batch = llama_batch_init(shape.tokens, n_embd, 1);
    batch.n_tokens = shape.tokens;
    std::memcpy(batch.embd, activation_f32.data(), activation_f32.size() * sizeof(float));
    for (int i = 0; i < shape.tokens; ++i) {
        batch.pos[i] = pos_offset + i;
        batch.n_seq_id[i] = 1;
        batch.seq_id[i][0] = 0;
        batch.logits[i] = (i == shape.tokens - 1) ? 1 : 0;
    }

    const int32_t decode_status = llama_decode(ctx, batch);
    llama_batch_free(batch);
    if (decode_status != 0) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << "llama_decode failed: " << decode_status << "\n";
        return 1;
    }

    float * logits = llama_get_logits_ith(ctx, -1);
    if (!logits) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << "llama_get_logits_ith returned null\n";
        return 1;
    }
    int best_token = 0;
    float best_logit = logits[0];
    for (int i = 1; i < n_vocab; ++i) {
        if (logits[i] > best_logit) {
            best_logit = logits[i];
            best_token = i;
        }
    }
    const std::string piece = token_to_piece(vocab, static_cast<llama_token>(best_token));
    const bool final = env_bool_value("CMESH_TERMINAL_FORCE_FINAL", true);
    resize_with_placeholder_tokens(session_tokens, static_cast<size_t>(pos_offset));
    session_tokens.push_back(static_cast<llama_token>(best_token));
    const size_t session_saved_bytes = save_stage_sequence_state(ctx, session_path, session_tokens, error);
    if (!error.empty()) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << error << "\n";
        return 1;
    }

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_terminal_decode\",\n"
        << "  \"status\": \"executed\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"stage_index\": " << stage_index << ",\n"
        << "  \"stage_start\": " << stage_start << ",\n"
        << "  \"stage_end\": " << stage_end << ",\n"
        << "  \"terminal_stage\": true,\n"
        << "  \"input_tensor\": {\n"
        << "    \"dtype\": \"" << json_escape(dtype) << "\",\n"
        << "    \"shape\": [";
    for (size_t i = 0; i < shape.dims.size(); ++i) {
        if (i > 0) {
            std::cout << ", ";
        }
        std::cout << shape.dims[i];
    }
    std::cout
        << "],\n"
        << "    \"bytes\": " << payload.size() << ",\n"
        << "    \"llama_batch_dtype\": \"f32\",\n"
        << "    \"llama_batch_bytes\": " << (activation_f32.size() * sizeof(float)) << "\n"
        << "  },\n"
        << "  \"logits\": {\n"
        << "    \"dtype\": \"f32\",\n"
        << "    \"shape\": [1, " << n_vocab << "],\n"
        << "    \"bytes\": " << (static_cast<size_t>(n_vocab) * sizeof(float)) << "\n"
        << "  },\n"
        << "  \"next_token_id\": " << best_token << ",\n"
        << "  \"next_token_text\": \"" << json_escape(piece) << "\",\n"
        << "  \"tokens\": [" << best_token << "],\n"
        << "  \"output\": \"" << json_escape(piece) << "\",\n"
        << "  \"final\": " << (final ? "true" : "false") << ",\n"
        << "  \"next_token_logit\": " << best_logit << ",\n"
        << "  \"selected_tensor_count\": " << (manifest.stage_tensor_count + manifest.boundary_tensor_count) << ",\n"
        << "  \"selected_bytes\": " << manifest.selected_bytes << ",\n"
        << "  \"kv_session\": {\n"
        << "    \"enabled\": " << (!session_path.empty() ? "true" : "false") << ",\n"
        << "    \"path\": \"" << json_escape(session_path) << "\",\n"
        << "    \"loaded_bytes\": " << session_loaded_bytes << ",\n"
        << "    \"saved_bytes\": " << session_saved_bytes << ",\n"
        << "    \"token_count\": " << session_tokens.size() << ",\n"
        << "    \"position_offset\": " << pos_offset << "\n"
        << "  },\n"
        << "  \"decode_status\": " << decode_status << ",\n"
        << "  \"guardrail\": \"first executable terminal-stage greedy token bridge; file-backed native sequence state enabled when CMESH_STAGE_SESSION_FILE is set; daemonized KV ownership still pending\"\n"
        << "}\n";

    llama_free(ctx);
    llama_model_free(model);
    llama_backend_free();
    return 0;
}

static int run_source_decode(const std::vector<std::string> & args) {
    const std::string model_path = arg_value(args, "--model");
    const std::string prompt = arg_value(args, "--prompt");
    const std::string output_path = arg_value(args, "--output-file");
    const int token_id = arg_int_value(args, "--token-id", -1);
    const int stage_start = arg_int_value(args, "--stage-start", -1);
    const int stage_end = arg_int_value(args, "--stage-end", -1);
    const int stage_index = arg_int_value(args, "--stage-index", 0);
    const int n_threads = arg_int_value(args, "--threads", 0);
    const int n_ctx_arg = arg_int_value(args, "--ctx", 0);

    if (model_path.empty()) {
        std::cerr << "--model is required for source-decode\n";
        return 2;
    }
    if (prompt.empty() && token_id < 0) {
        std::cerr << "--prompt or --token-id is required for source-decode\n";
        return 2;
    }
    if (output_path.empty()) {
        std::cerr << "--output-file is required for source-decode\n";
        return 2;
    }
    if (stage_start != 0 || stage_end < stage_start) {
        std::cerr << "source-decode requires a first-stage range starting at layer 0\n";
        return 2;
    }

    llama_backend_init();

    StageGGUFLoadPlan stage_plan;
    std::string stage_plan_error;
    if (!load_stage_gguf_plan(model_path, stage_plan, stage_plan_error)) {
        llama_backend_free();
        std::cerr << stage_plan_error << "\n";
        return 1;
    }
    if (stage_plan.stage_metadata && (stage_plan.stage_start != stage_start || stage_plan.stage_end != stage_end)) {
        llama_backend_free();
        std::cerr << "source-decode stage range does not match stage GGUF metadata\n";
        return 2;
    }

    llama_model_params metadata_params = llama_model_default_params();
    metadata_params.n_gpu_layers = 0;
    metadata_params.use_mmap = true;
    apply_stage_gguf_plan(metadata_params, stage_plan);
    llama_model * metadata_model = llama_model_load_from_file(model_path.c_str(), metadata_params);
    if (!metadata_model) {
        llama_backend_free();
        std::cerr << "failed to load model metadata: " << model_path << "\n";
        return 1;
    }
    const int n_layer = llama_model_n_layer(metadata_model);
    const int n_embd = llama_model_n_embd(metadata_model);
    const bool valid_range = stage_end < n_layer;
    if (!valid_range) {
        llama_model_free(metadata_model);
        llama_backend_free();
        std::cerr << "stage range exceeds model layer count\n";
        return 2;
    }

    const llama_vocab * vocab = llama_model_get_vocab(metadata_model);
    std::string input_mode = "prompt";
    std::vector<llama_token> tokens;
    if (token_id >= 0) {
        if (token_id >= llama_vocab_n_tokens(vocab)) {
            llama_model_free(metadata_model);
            llama_backend_free();
            std::cerr << "--token-id exceeds model vocabulary\n";
            return 2;
        }
        tokens.push_back(static_cast<llama_token>(token_id));
        input_mode = "token_id";
    } else {
        int32_t token_count = llama_tokenize(vocab, prompt.c_str(), static_cast<int32_t>(prompt.size()), nullptr, 0, true, true);
        if (token_count < 0) {
            token_count = -token_count;
        }
        if (token_count <= 0) {
            llama_model_free(metadata_model);
            llama_backend_free();
            std::cerr << "prompt produced no tokens\n";
            return 2;
        }
        tokens.resize(static_cast<size_t>(token_count));
        const int32_t encoded = llama_tokenize(vocab, prompt.c_str(), static_cast<int32_t>(prompt.size()), tokens.data(), token_count, true, true);
        if (encoded < 0 || encoded > token_count) {
            llama_model_free(metadata_model);
            llama_backend_free();
            std::cerr << "failed to tokenize prompt\n";
            return 2;
        }
        tokens.resize(static_cast<size_t>(encoded));
    }
    llama_model_free(metadata_model);
    metadata_model = nullptr;

    const TensorManifest manifest = build_tensor_manifest(model_path, stage_start, stage_end, true, stage_end == n_layer - 1);
    if (manifest.selected.empty()) {
        llama_backend_free();
        std::cerr << "selected tensor list is empty\n";
        return 2;
    }

    std::vector<const char *> allowlist;
    allowlist.reserve(manifest.selected.size());
    for (const auto & tensor : manifest.selected) {
        allowlist.push_back(tensor.name.c_str());
    }

    llama_model_params model_params = llama_model_default_params();
    model_params.n_gpu_layers = 0;
    model_params.use_mmap = true;
    model_params.cmesh_stage_tensor_allowlist = allowlist.data();
    model_params.cmesh_stage_tensor_allowlist_count = allowlist.size();
    model_params.cmesh_stage_layer_start = stage_start;
    model_params.cmesh_stage_layer_end = stage_end;
    model_params.cmesh_stage_partial_load = true;
    if (stage_plan.stage_metadata) {
        apply_stage_gguf_plan(model_params, stage_plan);
    }

    llama_model * model = llama_model_load_from_file(model_path.c_str(), model_params);
    if (!model) {
        llama_backend_free();
        std::cerr << "failed to load selected source stage tensors\n";
        return 1;
    }

    llama_context_params ctx_params = llama_context_default_params();
    ctx_params.n_ctx = stage_context_size(n_ctx_arg, std::max<int>(tokens.size(), 1));
    ctx_params.n_batch = static_cast<uint32_t>(tokens.size());
    ctx_params.n_ubatch = static_cast<uint32_t>(tokens.size());
    ctx_params.n_seq_max = 1;
    ctx_params.n_outputs_max = static_cast<uint32_t>(tokens.size());
    ctx_params.embeddings = true;
    ctx_params.pooling_type = LLAMA_POOLING_TYPE_NONE;
    if (n_threads > 0) {
        ctx_params.n_threads = n_threads;
        ctx_params.n_threads_batch = n_threads;
    }

    llama_context * ctx = llama_init_from_model(model, ctx_params);
    if (!ctx) {
        llama_model_free(model);
        llama_backend_free();
        std::cerr << "failed to create llama context for source decode\n";
        return 1;
    }

    const std::string session_path = stage_session_file();
    std::vector<llama_token> session_tokens;
    std::string session_error;
    const size_t session_loaded_bytes = load_stage_sequence_state(ctx, session_path, session_tokens, session_error);
    if (!session_error.empty()) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << session_error << "\n";
        return 1;
    }

    llama_batch batch = llama_batch_init(static_cast<int32_t>(tokens.size()), 0, 1);
    batch.n_tokens = static_cast<int32_t>(tokens.size());
    const char * step_raw = std::getenv("CMESH_STAGE_STEP");
    int step = 1;
    if (step_raw && step_raw[0] != '\0') {
        char * end = nullptr;
        const long parsed = std::strtol(step_raw, &end, 10);
        if (end != step_raw && *end == '\0' && parsed > 0 && parsed <= std::numeric_limits<int>::max()) {
            step = static_cast<int>(parsed);
        }
    }
    const int restored_pos_offset = next_sequence_position(ctx, session_tokens);
    const int pos_offset = restored_pos_offset > 0 ? restored_pos_offset : (token_id >= 0 ? std::max(step - 1, 0) : 0);
    for (int32_t i = 0; i < batch.n_tokens; ++i) {
        batch.token[i] = tokens[static_cast<size_t>(i)];
        batch.pos[i] = pos_offset + i;
        batch.n_seq_id[i] = 1;
        batch.seq_id[i][0] = 0;
        batch.logits[i] = 1;
    }

    const int32_t decode_status = llama_decode(ctx, batch);
    llama_batch_free(batch);
    if (decode_status != 0) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << "llama_decode failed: " << decode_status << "\n";
        return 1;
    }

    float * embeddings = llama_get_embeddings(ctx);
    if (!embeddings) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << "llama_get_embeddings returned null\n";
        return 1;
    }

    const size_t output_bytes = tokens.size() * static_cast<size_t>(n_embd) * sizeof(float);
    std::string error;
    if (!write_file(output_path, reinterpret_cast<const uint8_t *>(embeddings), output_bytes, error)) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << error << "\n";
        return 1;
    }

    resize_with_placeholder_tokens(session_tokens, static_cast<size_t>(pos_offset));
    session_tokens.insert(session_tokens.end(), tokens.begin(), tokens.end());
    const size_t session_saved_bytes = save_stage_sequence_state(ctx, session_path, session_tokens, session_error);
    if (!session_error.empty()) {
        llama_free(ctx);
        llama_model_free(model);
        llama_backend_free();
        std::cerr << session_error << "\n";
        return 1;
    }

    std::cout
        << "{\n"
        << "  \"kind\": \"cmesh.llamacpp_stage_source_decode\",\n"
        << "  \"status\": \"executed\",\n"
        << "  \"runtime\": \"llama.cpp\",\n"
        << "  \"model_path\": \"" << json_escape(model_path) << "\",\n"
        << "  \"stage_index\": " << stage_index << ",\n"
        << "  \"stage_start\": " << stage_start << ",\n"
        << "  \"stage_end\": " << stage_end << ",\n"
        << "  \"input_mode\": \"" << input_mode << "\",\n"
        << "  \"previous_token_id\": " << (token_id >= 0 ? std::to_string(token_id) : "null") << ",\n"
        << "  \"position_offset\": " << pos_offset << ",\n"
        << "  \"prompt_bytes\": " << prompt.size() << ",\n"
        << "  \"token_count\": " << tokens.size() << ",\n"
        << "  \"output_tensor\": {\n"
        << "    \"dtype\": \"f32\",\n"
        << "    \"shape\": [1, " << tokens.size() << ", " << n_embd << "],\n"
        << "    \"bytes\": " << output_bytes << ",\n"
        << "    \"path\": \"" << json_escape(output_path) << "\"\n"
        << "  },\n"
        << "  \"selected_tensor_count\": " << (manifest.stage_tensor_count + manifest.boundary_tensor_count) << ",\n"
        << "  \"selected_bytes\": " << manifest.selected_bytes << ",\n"
        << "  \"kv_session\": {\n"
        << "    \"enabled\": " << (!session_path.empty() ? "true" : "false") << ",\n"
        << "    \"path\": \"" << json_escape(session_path) << "\",\n"
        << "    \"loaded_bytes\": " << session_loaded_bytes << ",\n"
        << "    \"saved_bytes\": " << session_saved_bytes << ",\n"
        << "    \"token_count\": " << session_tokens.size() << ",\n"
        << "    \"position_offset\": " << pos_offset << "\n"
        << "  },\n"
        << "  \"decode_status\": " << decode_status << ",\n"
        << "  \"guardrail\": \"first executable local source-stage bridge; file-backed native sequence state enabled when CMESH_STAGE_SESSION_FILE is set; daemonized KV ownership still pending\"\n"
        << "}\n";

    llama_free(ctx);
    llama_model_free(model);
    llama_backend_free();
    return 0;
}

int main(int argc, char ** argv) {
    std::vector<std::string> args;
    args.reserve(argc > 0 ? static_cast<size_t>(argc - 1) : 0);
    for (int i = 1; i < argc; ++i) {
        args.emplace_back(argv[i]);
    }

    if (has_arg(args, "--help") || has_arg(args, "-h")) {
        print_usage(argv[0]);
        return 0;
    }

    if (has_arg(args, "--probe")) {
        print_probe("probe", "");
        return 0;
    }

    const std::string command = arg_value(args, "--command");
    const std::string input = arg_value(args, "--input");
    if (command.empty()) {
        print_usage(argv[0]);
        return 2;
    }

    if (command != "prepare" && command != "write-shard-bundle" && command != "inspect-shard-bundle" && command != "extract-shard-tensor" && command != "verify-shard-tensor-source" && command != "verify-shard-bundle-source" && command != "write-stage-gguf-shard" && command != "probe-stage-gguf-load" && command != "prefill" && command != "source-decode" && command != "decode" && command != "terminal-decode" && command != "resident-capabilities" && command != "resident-decode" && command != "resident-loop" && command != "complete" && command != "abort") {
        std::cerr << "unsupported command: " << command << "\n";
        return 2;
    }

    if (command == "resident-capabilities") {
        return run_resident_capabilities();
    }
    if (command == "resident-decode") {
        return run_resident_decode(args);
    }
    if (command == "resident-loop") {
        return run_resident_loop();
    }
    if (command == "prepare" && !arg_value(args, "--model").empty()) {
        return run_prepare(args);
    }
    if (command == "write-shard-bundle" && !arg_value(args, "--model").empty()) {
        return run_write_shard_bundle(args);
    }
    if (command == "inspect-shard-bundle" && !arg_value(args, "--bundle-file").empty()) {
        return run_inspect_shard_bundle(args);
    }
    if (command == "extract-shard-tensor" && !arg_value(args, "--bundle-file").empty()) {
        return run_extract_shard_tensor(args);
    }
    if (command == "verify-shard-tensor-source" && !arg_value(args, "--bundle-file").empty()) {
        return run_verify_shard_tensor_source(args);
    }
    if (command == "verify-shard-bundle-source" && !arg_value(args, "--bundle-file").empty()) {
        return run_verify_shard_bundle_source(args);
    }
    if (command == "write-stage-gguf-shard" && !arg_value(args, "--bundle-file").empty()) {
        return run_write_stage_gguf_shard(args);
    }
    if (command == "probe-stage-gguf-load" && !arg_value(args, "--model").empty()) {
        return run_probe_stage_gguf_load(args);
    }
    if (command == "source-decode" && !arg_value(args, "--model").empty()) {
        return run_source_decode(args);
    }
    if (command == "decode" && !arg_value(args, "--model").empty()) {
        return run_decode(args);
    }
    if (command == "terminal-decode" && !arg_value(args, "--model").empty()) {
        return run_terminal_decode(args);
    }

    print_probe(command, input);
    return 3;
}
