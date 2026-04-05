from parlant.core.tools import ToolResult


def get_available_drinks() -> ToolResult:
    return ToolResult(["Sprite", "Coca Cola"])


def get_available_toppings() -> ToolResult:
    return ToolResult(["Pepperoni", "Mushrooms", "Olives"])


def get_qualification_info() -> ToolResult:
    return ToolResult(
        {"qualification_info": "5+ years of experience"},
        canned_response_fields={"qualification_info": "5+ years of experience"},
    )


def get_return_status(session_id: str | None = None) -> ToolResult:
    return ToolResult({"status": "in_transit", "session_id": session_id})


def get_terrys_offering() -> ToolResult:
    return ToolResult("Terry offers leaf")


def check_stock(
    product_name: str | None = None,
    product_type: str | None = None,
    item_name: str | None = None,
    session_id: str | None = None,
) -> ToolResult:
    label = product_name or product_type or item_name or "item"
    return ToolResult({"item": label, "in_stock": True, "session_id": session_id, "all_available": True})


def generic_lookup(session_id: str | None = None) -> ToolResult:
    return ToolResult({"session_id": session_id, "result": "generic_lookup"})


def lock_card(session_id: str | None = None) -> ToolResult:
    return ToolResult({"result": "success", "session_id": session_id})


def send_confirmation_email(
    email: str | None = None,
    recipient_email: str | None = None,
    appointment_time: str | None = None,
    confirmation_message: str | None = None,
) -> ToolResult:
    target = email or recipient_email or "customer@example.com"
    return ToolResult({"recipient": target, "status": "sent", "time": appointment_time, "message": confirmation_message})


def reset_password(
    account_name: str | None = None,
    username: str | None = None,
    contact: str | None = None,
    email: str | None = None,
    phone_number: str | None = None,
) -> ToolResult:
    return ToolResult(
        {
            "status": "success",
            "account_name": account_name or username,
            "contact": contact or email or phone_number,
        }
    )


def book_flight(destination: str | None = None, passenger_id: int | None = None) -> ToolResult:
    return ToolResult({"destination": destination, "passenger_id": passenger_id, "status": "booked"})


def check_motorcycle_price(
    model: str | None = None,
    motorcycle: str | None = None,
    product_name: str | None = None,
    query: str | None = None,
) -> ToolResult:
    label = model or motorcycle or product_name or query or "motorcycle"
    return ToolResult({"vehicle": label, "price": "$10000"})


def check_vehicle_price(
    model: str | None = None,
    vehicle: str | None = None,
    product_name: str | None = None,
    query: str | None = None,
) -> ToolResult:
    label = model or vehicle or product_name or query or "vehicle"
    return ToolResult({"vehicle": label, "price": "$12000"})


def schedule_appointment(date: str | None = None) -> ToolResult:
    return ToolResult({"status": "scheduled", "date": date})


def search_electronic_products(keyword: str | None = None, vendor: str | None = None) -> ToolResult:
    return ToolResult({"keyword": keyword, "vendor": vendor, "results": ["Example Product"]})


def register_sweepstake(
    full_name: str | None = None,
    city: str | None = None,
    street: str | None = None,
    house_number: str | None = None,
    number_of_entries: int | None = None,
    donation_amount: int | None = None,
) -> ToolResult:
    return ToolResult(
        {
            "status": "success",
            "full_name": full_name,
            "city": city,
            "street": street,
            "house_number": house_number,
            "number_of_entries": number_of_entries,
            "donation_amount": donation_amount,
        }
    )


def try_unlock_card(session_id: str | None = None) -> ToolResult:
    _ = session_id
    return ToolResult({"failure": "need to specify the last 6 digits of the card"})
