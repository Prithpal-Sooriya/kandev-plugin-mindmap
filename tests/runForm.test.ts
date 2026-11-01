import { describe, it, expect } from "vitest";
import { pickProfile } from "../ui/src/canvas/runForm";
import type { AgentProfile } from "../ui/src/contract";

describe("pickProfile", () => {
  it("chooses pi first, then cursor, then claude", () => {
    const claude: AgentProfile = { id: "p1", name: "C", harness: "claude" };
    const cursor: AgentProfile = { id: "p2", name: "Cu", harness: "cursor" };
    const pi: AgentProfile = { id: "p3", name: "P", harness: "pi" };

    expect(pickProfile([claude, cursor, pi])).toBe("p3"); // pi
    expect(pickProfile([claude, cursor])).toBe("p2");     // cursor
    expect(pickProfile([claude])).toBe("p1");             // claude
    expect(pickProfile([cursor, pi, claude])).toBe("p3"); // pi
  });

  it("respects preferred profile", () => {
    const claude: AgentProfile = { id: "p1", name: "C", harness: "claude" };
    const pi: AgentProfile = { id: "p3", name: "P", harness: "pi" };

    expect(pickProfile([claude, pi], "p1")).toBe("p1");
  });

  it("falls back to first remaining if none match", () => {
    const other1: AgentProfile = { id: "p4", name: "O1", harness: "other" };
    const other2: AgentProfile = { id: "p5", name: "O2", harness: "other" };

    expect(pickProfile([other1, other2])).toBe("p4");
  });
});
