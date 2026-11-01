// Inline answer bar under a selected, unanswered question node (ask_human).
// The server writes the answer to the node and relays it to the asking agent.
import { ContractError, type MapNode } from "../contract";
import { h, React } from "../runtime";
import type { MapController } from "./controller";

export function needsAnswer(node: MapNode | undefined): node is MapNode & { question: NonNullable<MapNode["question"]> } {
  return !!node && node.kind === "question" && !!node.question && !node.question.answer && !node.deleted;
}

export function QuestionAnswer(props: { ctl: MapController; node: MapNode; x: number; y: number }) {
  const { ctl, node } = props;
  const [text, setText] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const options = node.question?.options ?? [];
  const asker = ctl.engine.participants.get(node.author.participant_id);

  const answer = async (value: string) => {
    const a = value.trim();
    if (!a || busy) return;
    setBusy(true);
    try {
      await ctl.host.api.answerQuestion(ctl.mapId, node.id, a);
      setText("");
      ctl.host.toast(`Answer sent${asker ? ` to ${asker.name}` : ""}.`, "success");
    } catch (e) {
      ctl.host.toast(e instanceof ContractError ? e.message : "Could not send the answer.", "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className="mm-node-toolbar mm-question-bar"
      style={{ transform: `translate(${props.x}px,${props.y}px) scale(calc(1 / var(--mm-k, 1)))` }}
      data-testid="mm-question-bar"
    >
      <span>{asker ? `${asker.name} asks` : "Answer"}</span>
      {options.map((o) => (
        <button key={o} type="button" className="mm-btn mm-btn-xs" disabled={busy} onClick={() => answer(o)}>{o}</button>
      ))}
      <input
        className="mm-input mm-question-input"
        value={text}
        maxLength={4000}
        placeholder={options.length ? "Or type an answer" : "Type an answer"}
        onInput={(e: Event) => setText((e.target as HTMLInputElement).value)}
        onKeyDown={(e: KeyboardEvent) => {
          e.stopPropagation();
          if (e.key === "Enter") answer(text);
        }}
        aria-label="Answer"
      />
      <button type="button" className="mm-btn mm-btn-xs mm-btn-primary" disabled={busy || !text.trim()} onClick={() => answer(text)}>Send</button>
    </div>
  );
}
