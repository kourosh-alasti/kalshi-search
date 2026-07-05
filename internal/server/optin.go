package server

// optInPage is a public landing page for 10DLC CTA verification. Reviewers
// must be able to visit this URL and see exactly how subscribers opt in.
const optInPage = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Kalshi Alerts — SMS Opt-In</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 640px; margin: 2rem auto; padding: 0 1rem; line-height: 1.5; color: #111; }
    h1 { font-size: 1.5rem; }
    h2 { font-size: 1.1rem; margin-top: 1.5rem; }
    .number { font-size: 1.25rem; font-weight: 600; }
    .disclosure { background: #f5f5f5; padding: 1rem; border-radius: 6px; margin: 1rem 0; }
    a { color: #0066cc; }
  </style>
</head>
<body>
  <h1>Kalshi Alerts — SMS Opt-In</h1>
  <p>Kalshi Alerts is a low-volume SMS notification service. Subscribers receive
  automated text messages about prediction markets listed on Kalshi, including
  market titles, prices, closing times, and links to view markets on kalshi.com.</p>

  <h2>How to subscribe</h2>
  <p>Text <strong>START</strong> to:</p>
  <p class="number">+1 (213) 905-4405</p>
  <p>You will receive a welcome message with a link to choose alert categories
  and optional subcategories. Alert messages begin only after you complete that
  step.</p>

  <div class="disclosure">
    <strong>Consent disclosure:</strong> By texting START, you agree to receive
    recurring automated SMS messages from Kalshi Alerts at the mobile number
    you text from. Message frequency varies. Message and data rates may apply.
    Consent is not required as a condition of any purchase. Reply
    <strong>STOP</strong> to cancel or <strong>HELP</strong> for help.
  </div>

  <h2>What you will receive</h2>
  <ul>
    <li>Market alert digests with title, price, closing time, and a kalshi.com link</li>
    <li>Service replies to your commands (category selection, PAUSE, STATUS, etc.)</li>
  </ul>
  <p>Alerts are informational only and are not financial or investment advice.</p>

  <h2>How to opt out</h2>
  <p>Text <strong>STOP</strong>, <strong>UNSUBSCRIBE</strong>, <strong>CANCEL</strong>,
  <strong>END</strong>, or <strong>QUIT</strong> at any time. You will receive one
  confirmation message and no further alerts.</p>

  <h2>Help</h2>
  <p>Text <strong>HELP</strong> or <strong>INFO</strong> for assistance.</p>

  <h2>Policies</h2>
  <p>
    <a href="/privacy">Privacy Policy</a> ·
    <a href="/terms">Terms and Conditions</a>
  </p>
</body>
</html>`
