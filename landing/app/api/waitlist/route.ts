import { Resend } from "resend";
import { NextResponse } from "next/server";

const resend = new Resend(process.env.RESEND_API_KEY);

export async function POST(request: Request) {
  try {
    const { email } = await request.json();

    if (!email || !email.includes("@")) {
      return NextResponse.json(
        { error: "Valid email required." },
        { status: 400 }
      );
    }

    await resend.emails.send({
      from: "Magpie <onboarding@resend.dev>",
      to: [email],
      subject: "You're on the Magpie early access list",
      html: `
        <div style="font-family: -apple-system, BlinkMacSystemFont, sans-serif; max-width: 480px; margin: 0 auto; padding: 40px 20px;">
          <h2 style="font-size: 20px; font-weight: 600; margin: 0 0 16px;">Welcome to Magpie.</h2>
          <p style="color: #555; font-size: 15px; line-height: 1.6; margin: 0 0 20px;">
            You've secured early access. We're onboarding design partners in small batches — we'll reach out when your instance is ready.
          </p>
          <p style="color: #555; font-size: 15px; line-height: 1.6; margin: 0 0 20px;">
            In the meantime, you can explore the source on <a href="https://github.com/Nedjagang/magpie" style="color: #6DB3F2;">GitHub</a>.
          </p>
          <p style="color: #999; font-size: 13px; margin: 24px 0 0;">— The Magpie Team</p>
        </div>
      `,
    });

    return NextResponse.json({ success: true });
  } catch (error) {
    console.error("Waitlist signup error:", error);
    return NextResponse.json(
      { error: "Something went wrong. Please try again." },
      { status: 500 }
    );
  }
}
