// Blooper Client-side Chat & SSE Controller

let activeAgent = "agent_creator";
let activeSessionId = "default";
let eventSource = null;

function selectAgent(name, displayName) {
    activeAgent = name;
    document.querySelectorAll('.agent-item').forEach(el => {
        el.classList.remove('active');
        if (el.dataset.agent === name) {
            el.classList.add('active');
        }
    });
    document.getElementById('active-agent-title').textContent = displayName;
    document.getElementById('messages').innerHTML = `
        <div class="message assistant">
            <div class="message-meta">${displayName}</div>
            <div class="message-content">Hello! I am ready to assist. How can I help you?</div>
        </div>
    `;
}

async function sendMessage(e) {
    if (e) e.preventDefault();
    const input = document.getElementById('user-input');
    const text = input.value.trim();
    if (!text) return;

    input.value = '';
    const messagesDiv = document.getElementById('messages');

    // Append user message
    const userMsg = document.createElement('div');
    userMsg.className = 'message user';
    userMsg.innerHTML = `<div class="message-meta">You</div><div class="message-content">${escapeHtml(text)}</div>`;
    messagesDiv.appendChild(userMsg);

    // Create placeholder for assistant response
    const assistantMsg = document.createElement('div');
    assistantMsg.className = 'message assistant';
    assistantMsg.innerHTML = `<div class="message-meta">${activeAgent}</div><div class="message-content" id="current-stream"><i>Thinking...</i></div>`;
    messagesDiv.appendChild(assistantMsg);
    messagesDiv.scrollTop = messagesDiv.scrollHeight;

    const streamTarget = assistantMsg.querySelector('#current-stream');

    try {
        const res = await fetch('/api/chat', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                agent: activeAgent,
                message: text,
                session_id: activeSessionId
            })
        });

        if (!res.ok) {
            const err = await res.text();
            streamTarget.innerHTML = `<span style="color: var(--danger)">Error: ${err}</span>`;
            return;
        }

        // Handle streaming response using fetch ReadableStream
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let accumulated = "";
        streamTarget.innerHTML = "";

        while (true) {
            const { value, done } = await reader.read();
            if (done) break;
            const chunk = decoder.decode(value, { stream: true });
            
            // Check for Server-Sent Events lines (data: ...)
            const lines = chunk.split('\n');
            for (const line of lines) {
                if (line.startsWith('data: ')) {
                    const dataStr = line.slice(6).trim();
                    if (dataStr === '[DONE]') continue;
                    try {
                        const parsed = JSON.parse(dataStr);
                        if (parsed.content) {
                            accumulated += parsed.content;
                            streamTarget.textContent = accumulated;
                        }
                        if (parsed.approval) {
                            renderApprovalCard(parsed.approval, assistantMsg);
                        }
                    } catch (err) {
                        // Plain text fallback
                        accumulated += dataStr;
                        streamTarget.textContent = accumulated;
                    }
                }
            }
            messagesDiv.scrollTop = messagesDiv.scrollHeight;
        }

        if (!accumulated && !streamTarget.innerHTML) {
            streamTarget.textContent = "(No text returned)";
        }
    } catch (err) {
        streamTarget.innerHTML = `<span style="color: var(--danger)">Network error: ${err.message}</span>`;
    }
}

function renderApprovalCard(approval, container) {
    const card = document.createElement('div');
    card.className = 'approval-card';
    card.id = `approval-${approval.id}`;
    card.innerHTML = `
        <div class="approval-title">⚠️ Action Requires Approval: ${escapeHtml(approval.tool_name)}</div>
        <div class="approval-body">${escapeHtml(approval.arguments)}</div>
        <div class="approval-actions">
            <button class="btn btn-approve" onclick="resolveApproval('${approval.id}', true)">Approve & Execute</button>
            <button class="btn btn-reject" onclick="resolveApproval('${approval.id}', false)">Reject</button>
        </div>
    `;
    container.appendChild(card);
}

async function resolveApproval(id, approve) {
    const card = document.getElementById(`approval-${id}`);
    if (!card) return;
    
    card.querySelector('.approval-actions').innerHTML = `<i>Processing...</i>`;
    try {
        const res = await fetch(`/api/approvals/${id}/${approve ? 'approve' : 'reject'}`, {
            method: 'POST'
        });
        const data = await res.json();
        if (approve) {
            card.innerHTML = `
                <div class="approval-title" style="color: var(--success)">✅ Approved & Executed</div>
                <div class="approval-body">${escapeHtml(data.output || 'Success')}</div>
            `;
        } else {
            card.innerHTML = `
                <div class="approval-title" style="color: var(--danger)">❌ Action Rejected</div>
            `;
        }
    } catch (err) {
        card.querySelector('.approval-actions').innerHTML = `<span style="color: var(--danger)">Error: ${err.message}</span>`;
    }
}

function escapeHtml(str) {
    return (str || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
