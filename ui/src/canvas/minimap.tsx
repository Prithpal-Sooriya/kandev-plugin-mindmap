import { h, React } from "../runtime";
import type { MapController } from "./controller";
import type { Camera } from "../layout/geometry";

export function Minimap(props: { ctl: MapController; region: { x: number; y: number; w: number; h: number } | null; onNavigate: (x: number, y: number) => void }) {
  const { ctl, region, onNavigate } = props;
  const bounds = ctl.display.layout.bounds;
  
  if (!region || bounds.w === 0 || bounds.h === 0) return null;
  
  const activeRuns = [...ctl.engine.runs.values()].filter(r => r.status === "running" || r.status === "waiting");
  const dots = activeRuns.map(r => {
    const p = ctl.engine.participants.get(r.participant_id);
    const b = ctl.display.layout.boxes.get(r.node_id);
    if (!p || !b) return null;
    return { id: r.id, x: b.x + b.w / 2, y: b.y + b.h / 2, color: p.color };
  }).filter(Boolean) as { id: string; x: number; y: number; color: string }[];
  
  const pad = 100;
  const bx = bounds.x - pad;
  const by = bounds.y - pad;
  const bw = bounds.w + pad * 2;
  const bh = bounds.h + pad * 2;
  
  const handlePointer = (e: any) => {
    if (e.buttons !== 1) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const rx = (e.clientX - rect.left) / rect.width;
    const ry = (e.clientY - rect.top) / rect.height;
    onNavigate(bx + rx * bw, by + ry * bh);
  };
  
  return (
    <div className="mm-minimap" onPointerDown={handlePointer} onPointerMove={handlePointer}>
      <svg viewBox={`${bx} ${by} ${bw} ${bh}`} preserveAspectRatio="xMidYMid meet">
        <rect x={bx} y={by} width={bw} height={bh} fill="transparent" />
        {/* Draw a simplified tree or just the bounds */}
        <rect x={bounds.x} y={bounds.y} width={bounds.w} height={bounds.h} fill="var(--bg-muted)" rx="20" />
        
        {/* Viewport */}
        <rect 
          x={region.x} y={region.y} width={region.w} height={region.h} 
          fill="none" stroke="var(--primary)" strokeWidth={Math.max(bw, bh) * 0.01} 
        />
        
        {/* Agent dots */}
        {dots.map(d => (
          <circle key={d.id} cx={d.x} cy={d.y} r={Math.max(bw, bh) * 0.02} fill={d.color} />
        ))}
      </svg>
    </div>
  );
}
