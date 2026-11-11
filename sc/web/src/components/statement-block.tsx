import { useEffect, useRef, type ReactNode } from "react"
import { Check, Copy } from "lucide-react"

import { Button } from "@/components/ui/button"
import { useCopy } from "@/lib/use-copy"
import { cn } from "@/lib/utils"

// A block of text to copy: a T-SQL statement in monospace, or a sentence for the client's IT in prose.
// When the clipboard is blocked the text is selected instead, so Ctrl+C works.
export function StatementBlock({ label, text, prose = false }: { label?: ReactNode; text: string; prose?: boolean }) {
  const { status, copy } = useCopy()
  const block = useRef<HTMLPreElement>(null)

  async function copyText() {
    const ok = await copy(text)
    if (!ok && block.current) window.getSelection()?.selectAllChildren(block.current)
  }

  return (
    <div className="grid gap-1.5 min-w-0">
      <div className="flex items-center justify-between gap-2">
        {label ? <div className="font-medium">{label}</div> : <span />}
        <Button variant="ghost" size="xs" onClick={copyText}>
          {status === "copied" ? <Check /> : <Copy />}
          {status === "copied" ? "Copied" : status === "refused" ? "Selected, press Ctrl+C" : "Copy"}
        </Button>
      </div>
      <pre
        ref={block}
        className={cn(
          "rounded-lg bg-muted p-3 whitespace-pre-wrap break-words max-h-56 overflow-auto",
          prose ? "font-sans text-sm" : "font-mono text-xs",
        )}
      >
        {text}
      </pre>
    </div>
  )
}

// When the browser refuses the clipboard: the text in a small read-only box, selected, for Ctrl+C.
export function CopyFallback({ text, onClose }: { text: string; onClose: () => void }) {
  const box = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    box.current?.focus()
    box.current?.select()
  }, [text])
  return (
    <div className="grid gap-1.5 min-w-0">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">The browser blocked copying. The text is selected: press Ctrl+C.</span>
        <Button variant="ghost" size="xs" onClick={onClose}>
          Close
        </Button>
      </div>
      <textarea
        ref={box}
        readOnly
        value={text}
        rows={6}
        aria-label="Text to copy"
        onFocus={(e) => e.currentTarget.select()}
        className="w-full resize-y rounded-lg border bg-muted p-2 font-mono text-xs text-foreground outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
      />
    </div>
  )
}
