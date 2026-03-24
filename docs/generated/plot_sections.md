### Message Outstanding

![Message Chain Outstanding Messages](../generated/message_outstanding.svg)

<details>
<summary>XY Plot Source: <code>message_outstanding</code></summary>
<pre><code class="language-lisp">
(xyplot message_outstanding
  (title &quot;Message Chain Outstanding Messages&quot;)
  (steps 24)
  (metric sent-minus-received))
</code></pre>
</details>

### Message Receives

![Message Chain Receives By Step](../generated/message_receives.svg)

<details>
<summary>XY Plot Source: <code>message_receives</code></summary>
<pre><code class="language-lisp">
(xyplot message_receives
  (title &quot;Message Chain Receives By Step&quot;)
  (steps 24)
  (metric receive-count))
</code></pre>
</details>

### Message Sends

![Message Chain Sends By Step](../generated/message_sends.svg)

<details>
<summary>XY Plot Source: <code>message_sends</code></summary>
<pre><code class="language-lisp">
(xyplot message_sends
  (title &quot;Message Chain Sends By Step&quot;)
  (steps 24)
  (metric send-count))
</code></pre>
</details>

### Queue Outstanding

![Outstanding Messages By Step](../generated/queue_outstanding.svg)

<details>
<summary>XY Plot Source: <code>queue_outstanding</code></summary>
<pre><code class="language-lisp">
(xyplot queue_outstanding
  (title &quot;Outstanding Messages By Step&quot;)
  (steps 100)
  (metric sent-minus-received))
</code></pre>
</details>

