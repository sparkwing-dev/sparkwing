#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <stdint.h>
#include <stdio.h>
#include <node_api.h>
#include <uv.h>

static napi_value fail(napi_env env, const char *operation, unsigned long code) {
  char message[160];
  snprintf(message, sizeof(message), "%s failed (%lu)", operation, code);
  napi_throw_error(env, NULL, message);
  return NULL;
}

static void close_descriptor(int fd) {
  uv_fs_t request;
  uv_fs_close(NULL, &request, fd, NULL);
  uv_fs_req_cleanup(&request);
}

static napi_value open_handles(napi_env env, napi_callback_info info) {
  size_t count = 2;
  napi_value args[2], result, value;
  HANDLE handles[2];
  int fds[2] = {-1, -1};
  if (napi_get_cb_info(env, info, &count, args, NULL, NULL) != napi_ok || count != 2) {
    napi_throw_type_error(env, NULL, "two protocol handle BigInts are required");
    return NULL;
  }
  for (size_t i = 0; i < 2; i++) {
    uint64_t raw = 0;
    bool lossless = false;
    if (napi_get_value_bigint_uint64(env, args[i], &raw, &lossless) != napi_ok || !lossless ||
        raw == 0 || raw == UINT64_MAX || (uint64_t)(uintptr_t)raw != raw) {
      napi_throw_type_error(env, NULL, "protocol handles must fit an unsigned native pointer");
      return NULL;
    }
    handles[i] = (HANDLE)(uintptr_t)raw;
    if (GetFileType(handles[i]) != FILE_TYPE_PIPE) {
      napi_throw_type_error(env, NULL, "protocol handle is not a pipe");
      return NULL;
    }
  }
  if (handles[0] == handles[1]) {
    napi_throw_type_error(env, NULL, "protocol handles must be distinct");
    return NULL;
  }
  for (size_t i = 0; i < 2; i++) {
    // safety: Imported handles must stop inheriting before author code can spawn tools.
    if (!SetHandleInformation(handles[i], HANDLE_FLAG_INHERIT, 0)) {
      return fail(env, "clear protocol handle inheritance", GetLastError());
    }
  }
  for (size_t i = 0; i < 2; i++) {
    // safety: Node's libuv owns its CRT fd table; the addon's CRT may be separate.
    fds[i] = uv_open_osfhandle(handles[i]);
    if (fds[i] < 0) {
      if (fds[0] >= 0) close_descriptor(fds[0]);
      for (size_t pending = i; pending < 2; pending++) CloseHandle(handles[pending]);
      return fail(env, "open protocol descriptor", (unsigned long)(-fds[i]));
    }
  }
  if (napi_create_array_with_length(env, 2, &result) != napi_ok) goto failure;
  for (uint32_t i = 0; i < 2; i++) {
    if (napi_create_int32(env, fds[i], &value) != napi_ok || napi_set_element(env, result, i, value) != napi_ok) goto failure;
  }
  return result;

failure:
  close_descriptor(fds[0]);
  close_descriptor(fds[1]);
  napi_throw_error(env, NULL, "cannot return protocol descriptors");
  return NULL;
}

static napi_value initialize(napi_env env, napi_value exports) {
  napi_value open;
  if (napi_create_function(env, "open", NAPI_AUTO_LENGTH, open_handles, NULL, &open) != napi_ok ||
      napi_set_named_property(env, exports, "open", open) != napi_ok) {
    napi_throw_error(env, NULL, "cannot initialize protocol bridge");
    return NULL;
  }
  return exports;
}

NAPI_MODULE(protocol, initialize)
