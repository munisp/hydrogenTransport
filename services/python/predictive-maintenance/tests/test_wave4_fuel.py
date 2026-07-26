from app.events import consumption_kg_per_100km

def test_consumption_pair():
    # 5% of a 30 kg tank over 10 km = 1.5 kg / 10 km = 15 kg/100km
    assert consumption_kg_per_100km(50.0, 45.0, 10.0, 30.0) == 15.0

def test_rejects_refuel_jump_and_jitter():
    assert consumption_kg_per_100km(10.0, 90.0, 10.0, 30.0) is None  # refuel
    assert consumption_kg_per_100km(50.0, 49.0, 0.2, 30.0) is None   # jitter
    assert consumption_kg_per_100km(60.0, 20.0, 10.0, 30.0) is None  # sensor artifact
    assert consumption_kg_per_100km(50.0, 45.0, 10.0, 0.0) is None   # no capacity
