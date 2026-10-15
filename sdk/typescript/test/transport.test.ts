import assert from "node:assert/strict";
import { test } from "node:test";
import { protocolDescriptors } from "../src/transport.ts";

test("Unix protocol transport has only descriptors 3 and 4", () => {
  assert.deepEqual(protocolDescriptors(["--sw-node-protocol"], "linux"), { requestFd: 3, frameFd: 4 });
  assert.deepEqual(protocolDescriptors(["--sw-node-protocol"], "darwin"), { requestFd: 3, frameFd: 4 });
  for (const argv of [
    [],
    ["extra"],
    ["--sw-node-protocol", "extra"],
    ["--sw-node-protocol", "--sw-node-protocol"],
    ["--sw-node-protocol", "--sw-protocol-handles", "3,4"],
  ]) {
    assert.throws(() => protocolDescriptors(argv, "linux"), /protocol|argument/i);
  }
});

test("Windows handles use exact decimal BigInts before native conversion", () => {
  let observed: bigint[] = [];
  const fds = protocolDescriptors(
    ["--sw-node-protocol", "--sw-protocol-handles", "9007199254740993,9007199254740995"],
    "win32",
    (request, frame) => {
      observed = [request, frame];
      return [7, 8];
    },
  );
  assert.deepEqual(fds, { requestFd: 7, frameFd: 8 });
  assert.deepEqual(observed, [9007199254740993n, 9007199254740995n]);
  assert.deepEqual(
    protocolDescriptors(["--sw-protocol-handles", "64,68", "--sw-node-protocol"], "win32", () => [9, 10]),
    { requestFd: 9, frameFd: 10 },
  );
});

test("Windows malformed, missing, repeated and invalid handles fail before opening", () => {
  let calls = 0;
  const bridge = (): [number, number] => {
    calls++;
    return [5, 6];
  };
  const values = [
    "",
    "3",
    "3,4,5",
    "3.5,4",
    "-3,4",
    "3,0",
    "3,3",
    " 3,4",
    "3,4 ",
    "0x40,68",
    "18446744073709551615,4",
    "18446744073709551616,4",
  ];
  for (const value of values) {
    assert.throws(() => protocolDescriptors(["--sw-node-protocol", "--sw-protocol-handles", value], "win32", bridge), /handle/i);
  }
  for (const argv of [
    ["--sw-node-protocol"],
    ["--sw-node-protocol", "--sw-protocol-handles"],
    ["--sw-protocol-handles", "3,4"],
    ["--sw-node-protocol", "--sw-protocol-handles", "3,4", "--sw-protocol-handles", "5,6"],
  ]) {
    assert.throws(() => protocolDescriptors(argv, "win32", bridge), /protocol|handle/i);
  }
  assert.equal(calls, 0);
});

test("Windows native conversion failures are propagated", () => {
  const failure = new Error("request handle is not a pipe");
  assert.throws(
    () =>
      protocolDescriptors(["--sw-node-protocol", "--sw-protocol-handles", "64,68"], "win32", () => {
        throw failure;
      }),
    (error) => error === failure,
  );
});
