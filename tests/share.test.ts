import { describe, expect, it } from "vitest";
import { dataCodewords, encodeQR, FORMAT_L_MASK0, readDataBits, readFormatBits } from "../ui/src/canvas/qr";
import { installCommand, shareDialogModel, sharedLabel } from "../ui/src/canvas/share";
import type { ShareLink } from "../ui/src/contract";

const link = (over: Partial<ShareLink>): ShareLink => ({
  id: "sl_1",
  map_id: "mp_1",
  provider: "cloudflared",
  status: "active",
  created_at: "2026-01-01T00:00:00Z",
  url: "https://demo.trycloudflare.com/m/mms1.abcdefghijklmnopqrstuvwxyz0123456789ABC/",
  ...over,
});

describe("share dialog", () => {
  it("shows the brew command when the tunnel is not installed", () => {
    const message = "cloudflared is not installed. Run: brew install cloudflared";
    expect(installCommand(message)).toBe("brew install cloudflared");
    expect(installCommand("brew install tailscale")).toBe("brew install tailscale");
    const model = shareDialogModel(null, { code: "not_installed", message });
    expect(model.phase).toBe("missing");
    expect(model.install).toBe("brew install cloudflared");
  });

  it("shows the link while a map is shared, and nothing once it stops", () => {
    expect(shareDialogModel(link({}), null).phase).toBe("shared");
    expect(shareDialogModel(link({ status: "stopped", url: undefined }), null).phase).toBe("idle");
    expect(shareDialogModel(null, null).phase).toBe("idle");
    const failed = shareDialogModel(link({ status: "error", url: undefined, error: "tunnel exited", install_hint: "brew install ngrok" }), null);
    expect(failed.phase).toBe("error");
    expect(failed.install).toBe("brew install ngrok");
  });

  it("labels the header chip", () => {
    expect(sharedLabel(0)).toBe("shared · 0 guests");
    expect(sharedLabel(1)).toBe("shared · 1 guest");
    expect(sharedLabel(3)).toBe("shared · 3 guests");
  });
});

describe("qr", () => {
  it("places byte-mode data and the published format bits", () => {
    const qr = encodeQR("hi");
    expect(qr.version).toBe(1);
    expect(qr.matrix.length).toBe(21);
    expect(qr.matrix.every((row) => row.length === 21)).toBe(true);
    // Finder: dark border, light ring, dark centre.
    expect(qr.matrix[0]![0]).toBe(true);
    expect(qr.matrix[1]![1]).toBe(false);
    expect(qr.matrix[2]![2]).toBe(true);
    expect(qr.matrix[13]![8]).toBe(true); // always-dark module at (8, size-8)
    expect(readFormatBits(qr.matrix)).toBe(FORMAT_L_MASK0);

    const back = readDataBits(qr.matrix);
    expect(back.slice(0, qr.bits.length)).toEqual(qr.bits);

    // "hi" in byte mode: mode 0100, count 2, then the two bytes, then 0000.
    const prefix = dataCodewords("hi", 1).slice(0, 6);
    expect([...prefix]).toEqual([0x40, 0x26, 0x86, 0x90, 0xec, 0x11]);
    const placed = bitsToBytes(qr.bits.slice(0, 19 * 8));
    expect(placed.slice(0, 6)).toEqual([...prefix]);
  });

  it("grows a version and keeps a square alignment centre", () => {
    const url = "https://demo.trycloudflare.com/m/mms1." + "a".repeat(43) + "/";
    const qr = encodeQR(url);
    expect(qr.version).toBeGreaterThan(1);
    expect(qr.matrix.length).toBe(17 + 4 * qr.version);
    const back = readDataBits(qr.matrix);
    expect(back.slice(0, qr.bits.length)).toEqual(qr.bits);
  });
});

function bitsToBytes(bits: boolean[]): number[] {
  const out: number[] = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) {
    let v = 0;
    for (let b = 0; b < 8; b++) v = (v << 1) | (bits[i + b] ? 1 : 0);
    out.push(v);
  }
  return out;
}
