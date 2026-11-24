import * as React from "react"
import { createPortal } from "react-dom"
import { ChevronDown, Check } from "lucide-react"
import { cn } from "../../lib/utils"

function optionLabel(value: React.ReactNode): string {
  if (typeof value === "string" || typeof value === "number") return String(value)
  if (Array.isArray(value)) return value.map(optionLabel).join("")
  if (React.isValidElement<{ children?: React.ReactNode }>(value)) return optionLabel(value.props.children)
  return ""
}

export interface SelectProps extends Omit<React.SelectHTMLAttributes<HTMLSelectElement>, 'onChange'> {
  onChange?: (e: { target: { value: string } } | React.ChangeEvent<HTMLSelectElement>) => void
  placeholder?: string
}

const Select = React.forwardRef<HTMLDivElement, SelectProps>(
  ({ className, children, value, onChange, placeholder = "Select...", disabled, ...props }, ref) => {
    const [isOpen, setIsOpen] = React.useState(false)
    const [dropdownStyle, setDropdownStyle] = React.useState<React.CSSProperties>({})
    const containerRef = React.useRef<HTMLDivElement>(null)
    const triggerRef = React.useRef<HTMLDivElement>(null)
    const dropdownRef = React.useRef<HTMLDivElement>(null)
    const nativeRef = React.useRef<HTMLSelectElement>(null)
    const listboxID = React.useId()
    const [highlighted, setHighlighted] = React.useState(-1)
    const [inferredLabel, setInferredLabel] = React.useState<string>()

    // Merge external ref with internal ref
    React.useImperativeHandle(ref, () => containerRef.current!)

    // Extract options from children
    const options = React.useMemo(() => {
      const opts: { value: string; label: string; disabled?: boolean }[] = []
      function collect(nodes: React.ReactNode) { React.Children.forEach(nodes, (child) => {
        if (React.isValidElement(child) && child.type === 'option') {
          const props = child.props as { value?: string | number; children?: React.ReactNode; disabled?: boolean }
          opts.push({
            value: props.value?.toString() || '',
            label: optionLabel(props.children) || props.value?.toString() || '',
            disabled: props.disabled
          })
        } else if (React.isValidElement<{ children?: React.ReactNode }>(child)) {
          collect(child.props.children)
        }
      }) }
      collect(children)
      return opts
    }, [children])

    const selectedOption = options.find(opt => opt.value === value?.toString())

    // Calculate dropdown position
    const updateDropdownPosition = React.useCallback(() => {
      if (!triggerRef.current) return

      const rect = triggerRef.current.getBoundingClientRect()
      const viewportHeight = window.innerHeight
      const viewportWidth = window.innerWidth
      const spaceBelow = viewportHeight - rect.bottom
      const spaceAbove = rect.top
      const dropdownMaxHeight = 240 // max-h-60 = 15rem = 240px
      const openUpward = spaceBelow < dropdownMaxHeight && spaceAbove > spaceBelow

      setDropdownStyle({
        position: 'fixed',
        left: Math.max(8, Math.min(rect.left, viewportWidth - Math.min(rect.width, viewportWidth - 16) - 8)),
        width: Math.min(rect.width, viewportWidth - 16),
        zIndex: 9999,
        ...(openUpward
          ? { bottom: viewportHeight - rect.top + 4 }
          : { top: rect.bottom + 4 }),
        maxHeight: Math.max(60, Math.min(dropdownMaxHeight, openUpward ? spaceAbove - 8 : spaceBelow - 8)),
      })
    }, [])

    React.useEffect(() => {
      const label = nativeRef.current?.labels?.[0]
      if (!label) return
      const copy = label.cloneNode(true) as HTMLElement
      copy.querySelectorAll("select,input,button,[role=combobox],svg").forEach(node => node.remove())
      setInferredLabel(copy.textContent?.trim() || undefined)
    }, [children])

    React.useEffect(() => {
      if (!isOpen) return
      const escape = (event: KeyboardEvent) => {
        if (event.key !== "Escape") return
        event.preventDefault()
        event.stopPropagation()
        setIsOpen(false)
        triggerRef.current?.focus()
      }
      window.addEventListener("keydown", escape, true)
      return () => window.removeEventListener("keydown", escape, true)
    }, [isOpen])

    React.useEffect(() => {
      if (isOpen && highlighted >= 0) dropdownRef.current?.querySelector<HTMLElement>(`[data-option-index="${highlighted}"]`)?.scrollIntoView({ block: "nearest" })
    }, [isOpen, highlighted])

    // Handle clicking/touching outside to close
    React.useEffect(() => {
      if (!isOpen) return

      const handleOutside = (event: MouseEvent | TouchEvent) => {
        const target = event.target as Node
        if (
          containerRef.current && !containerRef.current.contains(target) &&
          dropdownRef.current && !dropdownRef.current.contains(target)
        ) {
          setIsOpen(false)
        }
      }
      document.addEventListener("mousedown", handleOutside)
      document.addEventListener("touchstart", handleOutside)
      return () => {
        document.removeEventListener("mousedown", handleOutside)
        document.removeEventListener("touchstart", handleOutside)
      }
    }, [isOpen])

    // Update position on scroll/resize when open
    React.useEffect(() => {
      if (!isOpen) return

      updateDropdownPosition()

      const handleUpdate = (e: Event) => {
        // Skip position update if the scroll originated from the dropdown itself
        if (dropdownRef.current && dropdownRef.current.contains(e.target as Node)) {
          return
        }
        updateDropdownPosition()
      }
      const handleResize = () => updateDropdownPosition()
      window.addEventListener("scroll", handleUpdate, true)
      window.addEventListener("resize", handleResize)
      return () => {
        window.removeEventListener("scroll", handleUpdate, true)
        window.removeEventListener("resize", handleResize)
      }
    }, [isOpen, updateDropdownPosition])

    const handleToggle = () => {
      if (disabled) return
      if (!isOpen) {
        updateDropdownPosition()
        const selected = options.findIndex(option => option.value === value?.toString() && !option.disabled)
        setHighlighted(selected >= 0 ? selected : options.findIndex(option => !option.disabled))
      }
      setIsOpen(!isOpen)
    }

    const handleSelect = (optionValue: string) => {
      if (onChange) {
        // Simulate a change event
        const event = {
          target: { value: optionValue },
          currentTarget: { value: optionValue },
          bubbles: true,
          cancelable: true,
          type: 'change'
        } as unknown as React.ChangeEvent<HTMLSelectElement>
        onChange(event)
      }
      setIsOpen(false)
      triggerRef.current?.focus()
    }

    const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
      if (disabled || event.altKey || event.ctrlKey || event.metaKey) return
      if (event.key === "Tab") { setIsOpen(false); return }
      const enabled = options.map((option, index) => option.disabled ? -1 : index).filter(index => index >= 0)
      if (event.key === "ArrowDown" || event.key === "ArrowUp" || event.key === "Home" || event.key === "End") {
        event.preventDefault()
        updateDropdownPosition()
        if (!isOpen) {
          const selected = options.findIndex(option => option.value === value?.toString() && !option.disabled)
          setHighlighted(selected >= 0 ? selected : enabled[0] ?? -1)
          setIsOpen(true)
          return
        }
        const at = enabled.indexOf(highlighted)
        const next = event.key === "Home" ? 0 : event.key === "End" ? enabled.length - 1
          : event.key === "ArrowDown" ? (at + 1) % enabled.length : (at - 1 + enabled.length) % enabled.length
        setHighlighted(enabled[next] ?? -1)
      } else if (event.key === "Enter" || event.key === " ") {
        event.preventDefault()
        if (isOpen && highlighted >= 0 && !options[highlighted]?.disabled) handleSelect(options[highlighted].value)
        else handleToggle()
      } else if (event.key.length === 1) {
        const match = options.findIndex(option => !option.disabled && option.label.toLocaleLowerCase().startsWith(event.key.toLocaleLowerCase()))
        if (match >= 0) { event.preventDefault(); updateDropdownPosition(); setHighlighted(match); setIsOpen(true) }
      }
    }

    const dropdown = isOpen ? createPortal(
      <div
        ref={dropdownRef}
        id={listboxID}
        role="listbox"
        aria-label={props["aria-label"] || inferredLabel}
        className="overflow-auto custom-scrollbar rounded-xl border border-border/70 bg-popover/95 text-popover-foreground shadow-xl backdrop-blur-xl animate-in fade-in-0 zoom-in-95"
        style={{
          ...dropdownStyle,
          WebkitOverflowScrolling: 'touch',
          overscrollBehavior: 'contain',
          pointerEvents: 'auto',
        }}
        onWheel={(e) => {
          // Prevent wheel events from propagating to the dialog/body
          // This ensures the dropdown scrolls instead of the parent dialog
          const el = e.currentTarget
          const { scrollTop, scrollHeight, clientHeight } = el
          const atTop = scrollTop === 0 && e.deltaY < 0
          const atBottom = scrollTop + clientHeight >= scrollHeight && e.deltaY > 0
          // Only stop propagation if there's room to scroll or we're at boundary
          if (!atTop && !atBottom) {
            e.stopPropagation()
          }
        }}
      >
        <div className="p-1">
          {options.map((option, index) => (
            <div
              key={option.value}
              id={`${listboxID}-${index}`}
              data-option-index={index}
              role="option"
              aria-selected={option.value === value?.toString()}
              aria-disabled={option.disabled || undefined}
              className={cn(
                "relative flex w-full cursor-default select-none items-center rounded-md py-2 pl-2.5 pr-8 text-sm outline-none focus:bg-accent focus:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50 hover:bg-accent hover:text-accent-foreground cursor-pointer",
                option.value === value?.toString() && "bg-accent text-accent-foreground",
                highlighted === index && "bg-accent text-accent-foreground",
                option.disabled && "opacity-50 cursor-not-allowed pointer-events-none"
              )}
              onClick={() => !option.disabled && handleSelect(option.value)}
              onMouseEnter={() => !option.disabled && setHighlighted(index)}
              onMouseDown={event => event.preventDefault()}
            >
              <span className="truncate">{option.label}</span>
              {option.value === value?.toString() && (
                <span className="absolute right-2 flex h-3.5 w-3.5 items-center justify-center">
                  <Check className="h-4 w-4" />
                </span>
              )}
            </div>
          ))}
          {options.length === 0 && (
            <div className="py-2 px-2 text-sm text-muted-foreground text-center">暂无选项</div>
          )}
        </div>
      </div>,
      document.body
    ) : null

    return (
      <div
        className={cn("relative inline-block w-full", className)}
        ref={containerRef}
      >
        {/* Hidden native select for form submission compatibility if needed */}
        <select
          ref={nativeRef}
          className="sr-only"
          tabIndex={-1}
          aria-hidden="true"
          value={value}
          onChange={onChange as any}
          disabled={disabled}
          {...props}
        >
          {children}
        </select>

        {/* Custom Trigger */}
        <div
          ref={triggerRef}
          onClick={event => { event.preventDefault(); handleToggle(); triggerRef.current?.focus() }}
          onKeyDown={onKeyDown}
          className={cn(
            "flex h-9 w-full items-center justify-between rounded-lg border border-input/80 bg-card/75 px-3 py-2 text-sm shadow-sm ring-offset-background placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
            isOpen ? "ring-2 ring-ring ring-offset-2 border-primary" : "",
            className
            , disabled && "cursor-not-allowed opacity-50"
          )}
          role="combobox"
          aria-expanded={isOpen}
          aria-haspopup="listbox"
          aria-controls={isOpen ? listboxID : undefined}
          aria-activedescendant={isOpen && highlighted >= 0 ? `${listboxID}-${highlighted}` : undefined}
          aria-disabled={disabled || undefined}
          aria-label={props["aria-label"] || inferredLabel}
          aria-labelledby={props["aria-labelledby"]}
          tabIndex={disabled ? -1 : 0}
        >
          <span className={cn("block truncate", !selectedOption && "text-muted-foreground")}>
            {selectedOption ? selectedOption.label : placeholder}
          </span>
          <ChevronDown className="h-4 w-4 opacity-50 flex-shrink-0" />
        </div>

        {dropdown}
      </div>
    )
  }
)
Select.displayName = "Select"

export { Select }
