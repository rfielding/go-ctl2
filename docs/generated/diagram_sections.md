### A To B Sequence

![A To B Sequence](../generated/a_to_b_sequence.svg)

<details>
<summary>Mermaid Source: <code>a_to_b_sequence.mmd</code></summary>
<pre><code class="language-mermaid">
sequenceDiagram
    participant Client
    participant Relay
    participant Server
    Client--&gt;&gt;Relay: message(type=ping)
    Relay--&gt;&gt;Server: message(type=ping)
</code></pre>
</details>

### A To B State

![A To B State](../generated/a_to_b_state.svg)

<details>
<summary>Mermaid Source: <code>a_to_b_state.mmd</code></summary>
<pre><code class="language-mermaid">
flowchart TD
    subgraph Client
        direction TB
        C_start([start]) --&gt;|&quot;sent = ping&lt;br/&gt;become done&quot;| C_done([done])
    end

    subgraph Relay
        direction TB
        R_relay([relay]) --&gt;|&quot;recv msg&lt;br/&gt;become relay__wait_0&quot;| R_wait0([relay__wait_0])
        R_wait0 --&gt;|&quot;send msg&lt;br/&gt;become relay&quot;| R_relay
    end

    subgraph Server
        direction TB
        S_idle([idle]) --&gt;|&quot;received = ping&lt;br/&gt;become accepted&quot;| S_accepted([accepted])
    end

    C_done -. send ping .-&gt; R_relay
    R_wait0 -. send ping .-&gt; S_idle
</code></pre>
</details>

### Mm1 5 Queue Flow

![Mm1 5 Queue Flow](../generated/mm1_5_queue_flow.svg)

<details>
<summary>Mermaid Source: <code>mm1_5_queue_flow.mmd</code></summary>
<pre><code class="language-mermaid">
sequenceDiagram
    participant Client
    participant Queue
    Note over Client: dice branch
    alt arrival
        Client--&gt;&gt;Queue: req
        alt count &lt; 5
            Queue-&gt;&gt;Queue: count += 1
        else count = 5
            Queue-&gt;&gt;Queue: dropped_count += 1
        end
    else sleep
        Client-&gt;&gt;Client: last = sleep
    end
    Note over Queue: service branch when count &gt; 0
    alt service-complete
        Queue-&gt;&gt;Queue: count -= 1
        Queue-&gt;&gt;Queue: last_departure = service-complete
    else busy
        Queue-&gt;&gt;Queue: last_departure = busy
    end
</code></pre>
</details>

### Mm1 5 Queue State

![Mm1 5 Queue State](../generated/mm1_5_queue_state.svg)

<details>
<summary>Mermaid Source: <code>mm1_5_queue_state.mmd</code></summary>
<pre><code class="language-mermaid">
flowchart TD
    subgraph Client
        direction TB
        C_loop([loop]) --&gt;|&quot;dice&amp;lt;0.5&lt;br/&gt;last = sleep&lt;br/&gt;become loop&quot;| C_loop
        C_loop --&gt;|&quot;dice&amp;gt;=0.5&lt;br/&gt;send req&lt;br/&gt;last = arrival&lt;br/&gt;become loop&quot;| C_loop
    end

    subgraph Queue
        direction TB
        Q_wait([wait]) --&gt;|&quot;req and count = 0&lt;br/&gt;count += 1&lt;br/&gt;elapsed = 0&lt;br/&gt;become wait&quot;| Q_wait
        Q_wait --&gt;|&quot;req and 0 &amp;lt; count &amp;lt; 5&lt;br/&gt;count += 1&lt;br/&gt;become wait&quot;| Q_wait
        Q_wait --&gt;|&quot;req and count = 5&lt;br/&gt;dropped_count += 1&lt;br/&gt;become wait&quot;| Q_wait
        Q_wait --&gt;|&quot;count &amp;gt; 0 and dice&amp;lt;0.5&lt;br/&gt;count -= 1&lt;br/&gt;last_departure = service-complete&lt;br/&gt;become wait&quot;| Q_wait
        Q_wait --&gt;|&quot;count &amp;gt; 0 and dice&amp;gt;=0.5&lt;br/&gt;last_departure = busy&lt;br/&gt;become wait&quot;| Q_wait
    end

    C_loop -. arrival req .-&gt; Q_wait
</code></pre>
</details>

