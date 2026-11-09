import AppKit
import WebKit

final class Delegate: NSObject, NSApplicationDelegate, WKNavigationDelegate {
    var window: NSWindow!
    let text = NSTextView(frame: .zero)
    let field = NSTextField(frame: .zero)
    let other = NSTextField(frame: .zero)
    let secure = NSSecureTextField(frame: .zero)
    var web: WKWebView!
    var timer: Timer?
    var sequence = 0
    var busy = false
    var webReady = false
    var directory: URL!
    func applicationDidFinishLaunching(_ note: Notification) {
        guard CommandLine.arguments.count == 2 else { NSApp.terminate(nil); return }
        directory = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
        window = NSWindow(contentRect: NSRect(x: 160, y: 160, width: 760, height: 480), styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.title = "MiMac 输入兼容性测试"
        text.isRichText = false; text.font = .systemFont(ofSize: 16)
        text.wantsLayer = true
        text.layer?.borderColor = NSColor.separatorColor.cgColor
        text.layer?.borderWidth = 0.5
        text.setAccessibilityIdentifier("matrix.textview")
        field.setAccessibilityIdentifier("matrix.field")
        other.setAccessibilityIdentifier("matrix.other")
        secure.setAccessibilityIdentifier("matrix.secure")
        for control in [field as NSTextField, other] { control.font = .systemFont(ofSize: 16) }
        secure.font = .systemFont(ofSize: 16)

        let content = verticalStack(spacing: 14)
        content.edgeInsets = NSEdgeInsets(top: 16, left: 18, bottom: 16, right: 18)
        content.translatesAutoresizingMaskIntoConstraints = false
        window.contentView!.addSubview(content)
        NSLayoutConstraint.activate([
            content.leadingAnchor.constraint(equalTo: window.contentView!.leadingAnchor),
            content.trailingAnchor.constraint(equalTo: window.contentView!.trailingAnchor),
            content.topAnchor.constraint(equalTo: window.contentView!.topAnchor),
            content.bottomAnchor.constraint(lessThanOrEqualTo: window.contentView!.bottomAnchor),
        ])

        let native = verticalStack(spacing: 9)
        addFullWidth(sectionTitle("原生控件"), to: native)
        addFullWidth(labeledControl("多行", control: text, height: 76), to: native)
        let nativeRow = NSStackView(views: [
            labeledControl("文本框", control: field, height: 30),
            labeledControl("另一文本框", control: other, height: 30),
            labeledControl("安全输入", control: secure, height: 30),
        ])
        nativeRow.orientation = .horizontal
        nativeRow.alignment = .top
        nativeRow.distribution = .fillEqually
        nativeRow.spacing = 12
        addFullWidth(nativeRow, to: native)
        addFullWidth(native, to: content)

        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        web = WKWebView(frame: .zero, configuration: config)
        web.translatesAutoresizingMaskIntoConstraints = false
        web.navigationDelegate = self
        web.heightAnchor.constraint(equalToConstant: 230).isActive = true
        let webSection = verticalStack(spacing: 8)
        addFullWidth(sectionTitle("网页控件"), to: webSection)
        addFullWidth(web, to: webSection)
        addFullWidth(webSection, to: content)
        web.loadHTMLString("""
        <!doctype html>
        <html lang="zh-CN">
        <meta charset="utf-8">
        <style>
        :root { color-scheme: light dark; }
        * { box-sizing: border-box; }
        body { margin: 0; color: CanvasText; background: Canvas; font: 16px system-ui, -apple-system, sans-serif; }
        .grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; align-items: start; }
        .control { min-width: 0; }
        .rich-control { margin-top: 12px; }
        .label { display: block; margin-bottom: 4px; color: CanvasText; font-size: 12px; line-height: 1.2; }
        input, textarea, [contenteditable="true"] {
            display: block; width: 100%; min-height: 32px; padding: 4px 6px;
            border: 0.5px solid GrayText; border-radius: 4px;
            color: FieldText; background: Field; font: 16px system-ui, -apple-system, sans-serif;
        }
        textarea { height: 58px; }
        [contenteditable="true"] { min-height: 32px; }
        #rich { min-height: 92px; padding: 7px; }
        #rich p { margin: 2px 0; }
        #rich ul { margin: 4px 0; padding-left: 22px; }
        </style>
        <div class="grid">
            <div class="control"><label class="label" for="input">单行</label><input id="input" aria-label="matrix input"></div>
            <div class="control"><label class="label" for="textarea">多行</label><textarea id="textarea" aria-label="matrix textarea"></textarea></div>
            <div class="control"><span class="label">可编辑</span><div id="editable" contenteditable="true" role="textbox" aria-label="matrix editable"></div></div>
            <div class="control"><label class="label" for="password">密码</label><input id="password" type="password" aria-label="matrix password"></div>
        </div>
        <div class="rich-control">
            <span class="label">富文本</span>
            <div id="rich" contenteditable="true" role="textbox" aria-multiline="true" aria-label="matrix rich editor"><p data-node="paragraph">L|<strong>ol</strong><em>d</em>|R</p><ul data-node="list"><li><p><span contenteditable="false" data-decoration="bullet">•</span> keep item</p></li></ul><p data-node="paragraph"><span contenteditable="false" data-decoration="comment">[comment]</span>tail</p></div>
        </div>
        """, baseURL: nil)
        window.makeKeyAndOrderFront(nil); window.makeFirstResponder(text)
        NSApp.activate(ignoringOtherApps: true)
        timer = Timer.scheduledTimer(withTimeInterval: 0.03, repeats: true) { [weak self] _ in self?.poll() }
    }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        webReady = true
        try? String(ProcessInfo.processInfo.processIdentifier).write(to: directory.appendingPathComponent("pid"), atomically: true, encoding: .utf8)
    }
    func poll() {
        guard webReady, !busy,
              let data = try? Data(contentsOf: directory.appendingPathComponent("command.json")),
              let c = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
              let seq = c["seq"] as? Int, seq > sequence,
              let action = c["action"] as? String else { return }
        sequence = seq; busy = true
        let target = c["target"] as? String ?? "textview"
        if action == "quit" { NSApp.terminate(nil); return }
        if action == "snapshot" { respond(seq); return }
        if action == "reset" {
            text.string = "L|old|R"; field.stringValue = "L|old|R"; other.stringValue = "untouched"
            secure.stringValue = "" // no password input or value recording
            web.evaluateJavaScript("for (const id of ['input','textarea','editable']) { const e=document.getElementById(id); if(id==='editable')e.textContent='L|old|R';else e.value='L|old|R'; } document.getElementById('rich').innerHTML=`<p data-node=\"paragraph\">L|<strong>ol</strong><em>d</em>|R</p><ul data-node=\"list\"><li><p><span contenteditable=\"false\" data-decoration=\"bullet\">•</span> keep item</p></li></ul><p data-node=\"paragraph\"><span contenteditable=\"false\" data-decoration=\"comment\">[comment]</span>tail</p>`; document.getElementById('password').value='';") { [weak self] _, error in
                if let error { self?.respond(seq, error: String(describing:error)); return }
                self?.focus(target, seq: seq, reset: true)
            }
            return
        }
        if action == "focus" { focus(target, seq: seq, reset: false); return }
        if action == "focus_cold" { focusCold(target, seq: seq); return }
        if action == "selection" {
            if target == "textview" { text.setSelectedRange(NSRange(location: 0, length: 0)) }
            else if target == "field" { (window.firstResponder as? NSTextView)?.setSelectedRange(NSRange(location:0,length:0)) }
            else if ["input", "textarea", "editable", "rich"].contains(target) {
                let js = """
                (()=>{let e=document.getElementById('\(target)');let r=document.createRange();if('\(target)'==='editable'){r.setStart(e.firstChild,0);}else if('\(target)'==='rich'){r.setStart(e.querySelector('strong').firstChild,0);}else{e.setSelectionRange(0,0);return true;}r.collapse(true);let s=window.getSelection();s.removeAllRanges();s.addRange(r);return true;})()
                """
                web.evaluateJavaScript(js) { [weak self] _, error in
                    DispatchQueue.main.asyncAfter(deadline:.now()+0.05) { self?.respond(seq,error:error.map{String(describing:$0)}) }
                }
                return
            }
            respond(seq); return
        }
        respond(seq, error:"unknown action")
    }
    func focusCold(_ target: String, seq: Int) {
        guard target == "rich" else { respond(seq, error: "cold AX probe only supports rich target"); return }
        window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
        window.makeFirstResponder(web)
        let js = """
        (()=>{let e=document.getElementById('rich');e.focus();let a=e.querySelector('strong').firstChild;let b=e.querySelector('em').firstChild;let r=document.createRange();r.setStart(a,0);r.setEnd(b,b.length);let s=window.getSelection();s.removeAllRanges();s.addRange(r);return true;})()
        """
        // Return as soon as WebKit completes the focus/selection script. The
        // parent does not poll AXDOMIdentifier before the probe child calls Capture.
        web.evaluateJavaScript(js) { [weak self] _, error in
            self?.respond(seq, error: error.map { String(describing: $0) })
        }
    }
    func focus(_ target: String, seq: Int, reset: Bool) {
        window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
        switch target {
        case "textview": window.makeFirstResponder(text); if reset { text.setSelectedRange(NSRange(location:2,length:3)) }
        case "field": window.makeFirstResponder(field); if reset { (window.firstResponder as? NSTextView)?.setSelectedRange(NSRange(location:2,length:3)) }
        case "other": window.makeFirstResponder(other)
        case "secure": window.makeFirstResponder(secure)
        case "input", "textarea", "editable", "rich", "password":
            window.makeFirstResponder(web)
            let js = """
            (()=>{let e=document.getElementById('\(target)');e.focus();let r=document.createRange();if('\(target)'==='editable'){r.setStart(e.firstChild,2);r.setEnd(e.firstChild,5);let s=window.getSelection();s.removeAllRanges();s.addRange(r);}else if('\(target)'==='rich'){let a=e.querySelector('strong').firstChild;let b=e.querySelector('em').firstChild;r.setStart(a,0);r.setEnd(b,b.length);let s=window.getSelection();s.removeAllRanges();s.addRange(r);}else if('\(target)'!=='password'){e.setSelectionRange(2,5);}return true;})()
            """
            web.evaluateJavaScript(js) { [weak self] _, error in
                DispatchQueue.main.asyncAfter(deadline:.now()+0.15) { self?.respond(seq,error:error.map{String(describing:$0)}) }
            }
            return
        default: respond(seq,error:"unknown target"); return
        }
        DispatchQueue.main.asyncAfter(deadline:.now()+0.05) { [weak self] in self?.respond(seq) }
    }
    func respond(_ seq: Int, error: String? = nil) {
        // Only fixture-owned synthetic nonsecure content is inspected here.
        web.evaluateJavaScript("(()=>{const r=document.getElementById('rich');return {input:document.getElementById('input').value,textarea:document.getElementById('textarea').value,editable:document.getElementById('editable').textContent,rich:r.querySelector(':scope > p').innerText,richList:Array.from(r.querySelectorAll('ul li')).map(e=>e.textContent).join('|'),richTail:r.querySelector(':scope > p:last-of-type').textContent,richDecorations:Array.from(r.querySelectorAll('[data-decoration]')).map(e=>e.dataset.decoration+':'+e.getAttribute('contenteditable')+':'+e.textContent).join('|'),richStructure:Array.from(r.children).map(e=>e.tagName).join(','),focused:document.activeElement.id};})()") { [weak self] value, jsError in
            guard let self else { return }
            var state: [String: Any] = ["seq":seq,"pid":ProcessInfo.processInfo.processIdentifier,"textview":self.text.string,"field":self.field.currentEditor()?.string ?? self.field.stringValue,"other":self.other.stringValue,"web":value as? [String:Any] ?? [:]]
            if let e = error ?? jsError.map({String(describing:$0)}) { state["error"] = e }
            if let data = try? JSONSerialization.data(withJSONObject:state,options:[.sortedKeys]), (try? data.write(to:self.directory.appendingPathComponent("result.json"),options:.atomic)) != nil { }
            self.busy = false
        }
    }

    // NSStackView alignment chooses an edge; it does not stretch arranged views.
    func addFullWidth(_ view: NSView, to stack: NSStackView) {
        view.translatesAutoresizingMaskIntoConstraints = false
        stack.addArrangedSubview(view)
        view.widthAnchor.constraint(equalTo: stack.widthAnchor,
            constant: -(stack.edgeInsets.left + stack.edgeInsets.right)).isActive = true
    }

    func verticalStack(spacing: CGFloat) -> NSStackView {
        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.distribution = .fill
        stack.spacing = spacing
        return stack
    }

    func sectionTitle(_ title: String) -> NSTextField {
        let label = NSTextField(labelWithString: title)
        label.font = .systemFont(ofSize: 14, weight: .semibold)
        label.textColor = .labelColor
        return label
    }

    func labeledControl(_ title: String, control: NSView, height: CGFloat) -> NSStackView {
        let group = verticalStack(spacing: 4)
        let label = NSTextField(labelWithString: title)
        label.font = .systemFont(ofSize: 12)
        label.textColor = .secondaryLabelColor
        control.translatesAutoresizingMaskIntoConstraints = false
        control.heightAnchor.constraint(equalToConstant: height).isActive = true
        addFullWidth(label, to: group)
        addFullWidth(control, to: group)
        return group
    }
}
let app=NSApplication.shared
let delegate=Delegate()
app.delegate=delegate
app.setActivationPolicy(.regular)
app.run()
